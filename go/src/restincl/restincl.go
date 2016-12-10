package main

// Request types supported:
// - json (TypedJSON, TypedUBF)
// - plain text (TypedString)
// - binary (TypedCarray)

//Hmm we might need to put in channels a free ATMI contexts..
import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	u "ubftab"

	atmi "github.com/endurox-dev/endurox-go"
)

const (
	progsection = "RESTIN"
)

const (
	UNSET = -1
	FALSE = 0
	TRUE  = 1
)

//Error handling type
const (
	ERRORS_HTTP = 1 //Return error code in http
	ERRORS_TEXT = 2 //Return error as formatted text (from config)
	ERRORS_RAW  = 3 //Use the raw formatting (just another kind for text)
	ERRORS_JSON = 4 //Contact the json fields to main respons block.
	//Return the error code as UBF response (usable only in case if CONV_JSON2UBF used)
	ERRORS_JSONUBF = 5
)

//Conversion types resolved
const (
	CONV_JSON2UBF = 1
	CONV_TEXT     = 2
	CONV_JSON     = 3
	CONV_RAW      = 4
)

//Defaults
const (
	ERRORS_DEFAULT             = ERRORS_JSON
	NOTIMEOUT_DEFAULT          = false /* we will use default timeout */
	CONV_DEFAULT               = "json2ubf"
	CONV_INT_DEFAULT           = CONV_JSON2UBF
	ERRFMT_JSON_MSG_DEFAULT    = "errormsg=\"%s\""
	ERRFMT_JSON_CODE_DEFAULT   = "errorcode=\"%d\""
	ERRFMT_JSON_ONSUCC_DEFAULT = TRUE /* generate success message in JSON */
	ERRFMT_TEXT_DEFAULT        = "%d: %s"
	ERRFMT_RAW_DEFAULT         = "%d: %s"
	ASYNCCALL_DEFAULT          = FALSE
	WORKERS                    = 10 /* Number of worker processes */
)

//We will have most of the settings as defaults
//And then these settings we can override with
type ServiceMap struct {
	Svc string `json:"svc"`
	//TODO: Move bello to upper case... otherwise decoder does not work.
	Url    string
	Errors string `json:"errors"`
	//Above converted to consntant
	Errors_int       int
	Trantout         int64  `json:"trantout"` //If set, then using global transactions
	Notime           bool   `json:"notime"`
	Errfmt_text      string `json:"errfmt_text"`
	Errfmt_json_msg  string `json:"errfmt_json_msg"`
	Errfmt_json_code string `json:"errfmt_json_code"`
	//If set, then generate code/message for success too
	Errfmt_json_onsucc int16       `json:"errfmt_json_onsucc"`
	Errmap_http        string      `json:"errmap_http"`
	Errmap_http_hash   map[int]int //Lookup map for tp->http codes
	Asynccall          int16       `json:"asynccall"` //use tpacall()
	Conv               string      `json:"conv"`      //Conv mode
	Conv_int           int16       //Resolve conversion type
	//Request logging classify service
	Reqlogsvc string `json:"reqlogsvc"`
	//Error mapping Enduro/X error code (including * for all):http error code
	Errors_fmt_http_map_str string `json:"Errors_fmt_http_map"`
	Errors_fmt_http_map     map[string]int
}

var M_port int = atmi.FAIL
var M_ip string
var M_url_map map[string]ServiceMap

//map the atmi error code (numbers + *) to some http error
//We shall provide default mappings.

var M_defaults ServiceMap

/* TLS Settings: */
var M_tls_enable int16 = FALSE
var M_tls_cert_file string
var M_tls_key_file string

//Conversion types
var M_convs = map[string]int{

	"json2ubf": CONV_JSON2UBF,
	"text":     CONV_TEXT,
	"json":     CONV_JSON,
	"raw":      CONV_RAW,
}

var M_workers int
var M_ac *atmi.ATMICtx //Mainly shared for logging....

//Create a empy service object
/*
func newServiceMap() *ServiceMap {
	var ret ServiceMap

	ret.Errors = UNSET
	ret.Notime = UNSET
	ret.Trantout = UNSET
	ret.Errfmt_json_onsucc = UNSET
	ret.Asynccall = UNSET
	return &ret

}
*/

//Remap the error from string to int constant
//for better performance...
func remapErrors(svc *ServiceMap) error {

	switch svc.Errors {
	case "http":
		svc.Errors_int = ERRORS_HTTP
		break
	case "json":
		svc.Errors_int = ERRORS_JSON
		break
	case "jsonubf":
		svc.Errors_int = ERRORS_JSONUBF
		break
	case "text":
		svc.Errors_int = ERRORS_TEXT
		break
	default:
		return errors.New(fmt.Sprintf("Unsupported error type [%s]", svc.Errors))
	}

	return nil
}

//Run the listener
func apprun(ac *atmi.ATMICtx) error {

	var err error
	//TODO: Some works needed for TLS...
	listen_on := fmt.Sprintf("%s:%d", M_ip, M_port)
	ac.TpLog(atmi.LOG_INFO, "About to listen on: (ip: %s, port: %d) %s",
		M_ip, M_port, listen_on)
	if TRUE == M_tls_enable {

		/* To prepare cert (self-signed) do following steps:
		 * - TODO
		 */
		err := http.ListenAndServeTLS(listen_on, M_tls_cert_file, M_tls_key_file, nil)
		ac.TpLog(atmi.LOG_ERROR, "ListenAndServeTLS() failed: %s", err)
	} else {
		err := http.ListenAndServe(listen_on, nil)
		ac.TpLog(atmi.LOG_ERROR, "ListenAndServe() failed: %s", err)
	}

	return err
}

//Init function, read config (with CCTAG)

func DispatchRequest(w http.ResponseWriter, req *http.Request) {
	M_ac.TpLog(atmi.LOG_DEBUG, "URL [%s] getting free goroutine", req.URL)

	var call HTTPCall

	call.w = w
	call.req = req

	//	nr := <-M_freechan
	nr := <-M_freechan

	svc := M_url_map[req.URL.String()]

	M_ac.TpLogInfo("Got free goroutine, nr %d", nr)

	//M_waitjobchan[nr] <- call
	//Get the free ATMI context

	HandleMessage(M_ctxs[nr], &svc, w, req)

	M_ac.TpLogInfo("Request processing done %d... releasing the context", nr)

	M_freechan <- nr

}

//Map the ATMI Errors to Http errors
//Format: <atmi_err>:<http_err>,<*>:<http_err>
//* - means any other unmapped ATMI error
//@param svc	Service map
func parseHttpErrorMap(ac *atmi.ATMICtx, svc *ServiceMap) error {

	svc.Errors_fmt_http_map = make(map[string]int)
	ac.TpLogDebug("Splitting error mapping string [%s]",
		svc.Errors_fmt_http_map_str)

	parsed := regexp.MustCompile(", *").Split(svc.Errors_fmt_http_map_str, -1)

	for index, element := range parsed {
		ac.TpLogDebug("Got pair [%s] at %d", element, index)

		pair := regexp.MustCompile(": *").Split(element, -1)

		pair_len := len(pair)

		if pair_len < 2 || pair_len > 2 {
			ac.TpLogError("Invalid http error pair: [%s] "+
				"parsed into %d elms", element, pair_len)

			return errors.New(fmt.Sprintf("Invalid http error pair: [%s] "+
				"parsed into %d elms", element, pair_len))
		}

		number, err := strconv.ParseInt(pair[1], 10, 0)

		if err != nil {
			ac.TpLogError("Failed to parse http error code %s (%s)",
				pair[1], err)
			return errors.New(fmt.Sprintf("Failed to parse http error code %s (%s)",
				pair[1], err))
		}

		//Add to hash
		svc.Errors_fmt_http_map[pair[0]] = int(number)
	}

	return nil
}

//Un-init function
func appinit(ac *atmi.ATMICtx) error {
	//runtime.LockOSThread()

	M_url_map = make(map[string]ServiceMap)

	//Setup default configuration
	M_defaults.Errors_int = ERRORS_DEFAULT
	M_defaults.Notime = NOTIMEOUT_DEFAULT
	M_defaults.Conv = CONV_DEFAULT
	M_defaults.Conv_int = CONV_INT_DEFAULT
	M_defaults.Errfmt_json_msg = ERRFMT_JSON_MSG_DEFAULT
	M_defaults.Errfmt_json_code = ERRFMT_JSON_CODE_DEFAULT
	M_defaults.Errfmt_json_onsucc = ERRFMT_JSON_ONSUCC_DEFAULT
	M_defaults.Errfmt_text = ERRFMT_TEXT_DEFAULT
	M_defaults.Asynccall = ASYNCCALL_DEFAULT

	M_workers = WORKERS

	if err := ac.TpInit(); err != nil {
		return errors.New(err.Error())
	}

	//Get the configuration

	buf, err := ac.NewUBF(16 * 1024)
	if nil != err {
		ac.TpLog(atmi.LOG_ERROR, "Failed to allocate buffer: [%s]", err.Error())
		return errors.New(err.Error())
	}

	buf.BChg(u.EX_CC_CMD, 0, "g")
	buf.BChg(u.EX_CC_LOOKUPSECTION, 0, fmt.Sprintf("%s/%s", progsection, os.Getenv("NDRX_CCTAG")))

	if _, err := ac.TpCall("@CCONF", buf, 0); nil != err {
		ac.TpLog(atmi.LOG_ERROR, "ATMI Error %d:[%s]\n", err.Code(), err.Message())
		return errors.New(err.Error())
	}

	buf.TpLogPrintUBF(atmi.LOG_DEBUG, "Got configuration.")

	//Set the parameters (ip/port/services)

	occs, _ := buf.BOccur(u.EX_CC_KEY)
	// Load in the config...
	for occ := 0; occ < occs; occ++ {
		ac.TpLog(atmi.LOG_DEBUG, "occ %d", occ)
		fld_name, err := buf.BGetString(u.EX_CC_KEY, occ)

		if nil != err {
			ac.TpLog(atmi.LOG_ERROR, "Failed to get field "+
				"%d occ %d", u.EX_CC_KEY, occ)
			return errors.New(err.Error())
		}

		ac.TpLog(atmi.LOG_DEBUG, "Got config field [%s]", fld_name)

		switch fld_name {

		case "workers":
			M_workers, _ = buf.BGetInt(u.EX_CC_VALUE, occ)
			break
		case "port":
			M_port, _ = buf.BGetInt(u.EX_CC_VALUE, occ)
			break
		case "ip":
			M_ip, _ = buf.BGetString(u.EX_CC_VALUE, occ)
			break
		case "tls_enable":
			M_tls_enable, _ = buf.BGetInt16(u.EX_CC_VALUE, occ)
			break
		case "tls_cert_file":
			M_tls_cert_file, _ = buf.BGetString(u.EX_CC_VALUE, occ)
			break
		case "tls_key_file":
			M_tls_key_file, _ = buf.BGetString(u.EX_CC_VALUE, occ)
			break
		case "defaults":
			//Override the defaults
			json_default, _ := buf.BGetByteArr(u.EX_CC_VALUE, occ)

			jerr := json.Unmarshal(json_default, &M_defaults)
			if jerr != nil {
				ac.TpLog(atmi.LOG_ERROR,
					fmt.Sprintf("Failed to parse defaults: %s", jerr))
				return jerr
			}

			if M_defaults.Errors_fmt_http_map_str != "" {
				if jerr := parseHttpErrorMap(ac, &M_defaults); err != nil {
					return jerr
				}
			}
			break
		default:
			//Assign the defaults

			//Load routes...
			if strings.HasPrefix(fld_name, "/") {
				cfg_val, _ := buf.BGetString(u.EX_CC_VALUE, occ)

				ac.TpLogInfo("Got route config [%s]", cfg_val)

				tmp := M_defaults

				//Override the stuff from current config

				//err := json.Unmarshal(cfg_val, &tmp)
				decoder := json.NewDecoder(strings.NewReader(cfg_val))
				//conf := Config{}
				err := decoder.Decode(&tmp)

				if err != nil {
					ac.TpLog(atmi.LOG_ERROR,
						fmt.Sprintf("Failed to parse config key %s: %s",
							fld_name, err))
					return err
				}

				ac.TpLog(atmi.LOG_DEBUG,
					"Got route: URL [%s] -> Service [%s]",
					fld_name, tmp.Svc)
				tmp.Url = fld_name

				//Parse http errors for
				if tmp.Errors_fmt_http_map_str != "" {
					if jerr := parseHttpErrorMap(ac, &tmp); err != nil {
						return jerr
					}
				}

				M_url_map[fld_name] = tmp
				//Add to HTTP listener
				http.HandleFunc(fld_name, DispatchRequest)
			}
			break
		}

	}

	if atmi.FAIL == M_port || "" == M_ip {
		ac.TpLog(atmi.LOG_ERROR, "Invalid config: missing ip (%s) or port (%d)",
			M_ip, M_port)
		return errors.New("Invalid config: missing ip or port")
	}

	//Check the TLS settings
	if TRUE == M_tls_enable && (M_tls_cert_file == "" || M_tls_key_file == "") {

		ac.TpLog(atmi.LOG_ERROR, "Invalid TLS settigns missing cert "+
			"(%s) or keyfile (%s) ", M_tls_cert_file, M_tls_key_file)

		return errors.New("Invalid config: missing ip or port")
	}

	//Add the default erorr mappings
	if M_defaults.Errors_fmt_http_map_str == "" {

		/*
					Errors to map:

			atmi.go:	TPEABORT      = 1
			atmi.go:	TPEBADDESC    = 2
			atmi.go:	TPEBLOCK      = 3
			atmi.go:	TPEINVAL      = 4
			atmi.go:	TPELIMIT      = 5
			atmi.go:	TPENOENT      = 6
			atmi.go:	TPEOS         = 7
			atmi.go:	TPEPERM       = 8
			atmi.go:	TPEPROTO      = 9
			atmi.go:	TPESVCERR     = 10
			atmi.go:	TPESVCFAIL    = 11
			atmi.go:	TPESYSTEM     = 12
			atmi.go:	TPETIME       = 13
			atmi.go:	TPETRAN       = 14
			atmi.go:	TPERMERR      = 16
			atmi.go:	TPEITYPE      = 17
			atmi.go:	TPEOTYPE      = 18
			atmi.go:	TPERELEASE    = 19
			atmi.go:	TPEHAZARD     = 20
			atmi.go:	TPEHEURISTIC  = 21
			atmi.go:	TPEEVENT      = 22
			atmi.go:	TPEMATCH      = 23
			atmi.go:	TPEDIAGNOSTIC = 24
			atmi.go:	TPEMIB        = 25
		*/

		//https://golang.org/src/net/http/status.go
		M_defaults.Errors_fmt_http_map = make(map[string]int)
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEABORT)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEBADDESC)] = http.StatusBadRequest
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEBLOCK)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEINVAL)] = http.StatusBadRequest
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPELIMIT)] = http.StatusRequestEntityTooLarge
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPENOENT)] = http.StatusNotFound
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEOS)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEPERM)] = http.StatusUnauthorized
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEPROTO)] = http.StatusBadRequest

		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPESVCERR)] = http.StatusBadGateway
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPESVCFAIL)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPESYSTEM)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPETIME)] = http.StatusGatewayTimeout
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPETRAN)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPERMERR)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEITYPE)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEOTYPE)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPERELEASE)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEHAZARD)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEHEURISTIC)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEEVENT)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEMATCH)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEDIAGNOSTIC)] = http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEMIB)] = http.StatusInternalServerError

		//Anything other goes to server error.
		M_defaults.Errors_fmt_http_map["*"] = http.StatusInternalServerError

	}

	ac.TpLogInfo("About to init woker pool, number of workers: %d", M_workers)

	InitPool(ac)

	return nil
}

//Service Main

func main() {

	var err atmi.ATMIError
	M_ac, err = atmi.NewATMICtx()

	if nil != err {
		fmt.Fprintf(os.Stderr, "Failed to allocate cotnext %s!\n", err)
		os.Exit(atmi.FAIL)
	}

	if err := appinit(M_ac); nil != err {
		os.Exit(atmi.FAIL)
	}
	M_ac.TpLogWarn("REST Incoming init ok - serving...")

	if err := apprun(M_ac); nil != err {
		os.Exit(atmi.FAIL)
	}

	os.Exit(atmi.SUCCEED)
}
