/**
 * @brief Enduro/X Incoming http REST handler (HTTP server, XATMI client)
 *
 * @file restincl.go
 */
/* -----------------------------------------------------------------------------
 * Enduro/X Middleware Platform for Distributed Transaction Processing
 * Copyright (C) 2009-2016, ATR Baltic, Ltd. All Rights Reserved.
 * Copyright (C) 2017-2018, Mavimax, Ltd. All Rights Reserved.
 * This software is released under one of the following licenses:
 * AGPL or Mavimax's license for commercial use.
 * -----------------------------------------------------------------------------
 * AGPL license:
 *
 * This program is free software; you can redistribute it and/or modify it under
 * the terms of the GNU Affero General Public License, version 3 as published
 * by the Free Software Foundation;
 *
 * This program is distributed in the hope that it will be useful, but WITHOUT ANY
 * WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A
 * PARTICULAR PURPOSE. See the GNU Affero General Public License, version 3
 * for more details.
 *
 * You should have received a copy of the GNU Affero General Public License along
 * with this program; if not, write to the Free Software Foundation, Inc.,
 * 59 Temple Place, Suite 330, Boston, MA 02111-1307 USA
 *
 * -----------------------------------------------------------------------------
 * A commercial use license is available from Mavimax, Ltd
 * contact@mavimax.com
 * -----------------------------------------------------------------------------
 */
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
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	u "ubftab"

	atmi "github.com/endurox-dev/endurox-go"
)

/*
#include <signal.h>
*/
import "C"

const (
	progsection = "@restin"
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
	ERRORS_JSON2UBF  = 5
	ERRORS_JSON2VIEW = 6
	ERRORS_EXT       = 7 //External mode errors, direct UBF error codes, services
)

const (
	ERRSRC_FINMAN  = "F" //Input mandatory filter failed
	ERRSRC_SERVICE = "S" //Error source is target service
	ERRSRC_RESTIN  = "R" //Error source is rest-in internal error
)

//Conversion types resolved
const (
	CONV_JSON2UBF  = 1
	CONV_TEXT      = 2
	CONV_JSON      = 3
	CONV_RAW       = 4
	CONV_JSON2VIEW = 5
	CONV_STATIC    = 6 //Serving static content
	CONV_EXT       = 7 //External services, raw FML buffers
)

//Defaults
const (
	ERRORS_DEFAULT             = ERRORS_JSON
	NOTIMEOUT_DEFAULT          = false /* we will use default timeout */
	CONV_DEFAULT               = "json2ubf"
	CONV_INT_DEFAULT           = CONV_JSON2UBF
	ERRFMT_JSON_MSG_DEFAULT    = "\"error_message\":\"%s\""
	ERRFMT_JSON_CODE_DEFAULT   = "\"error_code\":%d"
	ERRFMT_JSON_ONSUCC_DEFAULT = true /* generate success message in JSON */
	ERRFMT_VIEW_ONSUCC_DEFAULT = true /* generate success message in VIEW */
	ERRFMT_TEXT_DEFAULT        = "%d: %s"
	ASYNCCALL_DEFAULT          = false
	STREAM_DEFAULT             = false
	WORKERS                    = 10 /* Number of worker processes */
)

//We will have most of the settings as defaults
//And then these settings we can override with
type ServiceMap struct {
	Svc    string `json:"svc"`
	Url    string
	Errors string `json:"errors"`
	//Above converted to consntant
	Errors_int       int
	Notime           bool   `json:"notime"`
	Errfmt_text      string `json:"errfmt_text"`
	Errfmt_json_msg  string `json:"errfmt_json_msg"`
	Errfmt_json_code string `json:"errfmt_json_code"`
	//If set, then generate code/message for success too
	Errfmt_json_onsucc bool `json:"errfmt_json_onsucc"`

	//In case of json2view errors, we install the return
	//code direclty in the given fields
	Errfmt_view_msg    string `json:"errfmt_view_msg"`
	Errfmt_view_code   string `json:"errfmt_view_code"`
	Errfmt_view_onsucc bool   `json:"errfmt_view_onsucc"`

	//Install in response non null fields only
	View_notnull bool `json:"view_notnull"`

	View_flags int64 //Flags used for VIEW2JSON

	//Response view, if in original buffer fields defined in
	//errfmt_view_msg and errfmt_view_code are not found.
	//Must be set in case of 'async' if 'asyncecho' not set. In all other
	//cases for example if json2view errors are used, system will try
	//to install the error code in original buffer (either from service
	//response or parsed incomming msg)
	//In case if errfmt_view_rsp_first then errfmt_view_rsp is mandatory too
	//and errors are always returned within the 'errfmt_view_rsp' view struct
	Errfmt_view_rsp string `json:"errfmt_view_rsp"`
	//In case of normal calls, this will be set only if 'errfmt_view_onsucc' is
	//set, otherwise if there is no error, then normal response object is returned
	Errfmt_view_rsp_first bool `json:"errfmt_view_rsp_first"`

	Asynccall bool   `json:"async"`     //use tpacall()
	Asyncecho bool   `json:"asyncecho"` //echo message in async mode
	Conv      string `json:"conv"`      //Conv mode
	Conv_int  int    //Resolve conversion type
	//Request logging classify service
	Reqlogsvc string `json:"reqlogsvc"`
	//Error mapping Enduro/X error code (including * for all):http error code
	Errors_fmt_http_map_str string `json:"errors_fmt_http_map"`
	Errors_fmt_http_map     map[string]int
	Noreqfilersp            bool `json:"noreqfilersp"` //Do not sent request file in respones
	Echo                    bool `json:"echo"`         //Echo request buffer back
	//URL format
	Format   string `json:"format"`   // "r" or "regexp" for regexp format
	UrlField string `json:"urlfield"` //Field for URL in case of CONV_JSON2UBF and CONV_JSON

	// Parsing request headers/Cookies
	Parseheaders    bool   `json:"parseheaders"`      // Default false
	Parsecookies    bool   `json:"parsecookies"`      // Default false
	Parseform       bool   `json:"parseform"`         // Parse form data and load into UBF
	Fileupload      bool   `json:"fileupload"`        // This url end-point is used for file upload
	Tempdir         string `json:"tempdir"`           // Temporary folder where to store uploaded files
	JsonCookieField string `json:"json_cookie_field"` //Field for Cookie object in case of CONV_JSON
	JsonHeaderField string `json:"json_header_field"` //Field for Headers in case of CONV_JSON

	//For ext mode:
	Finman     string `json:"finman"` // Mandatory incoming services
	Finman_arr []string
	Finopt     string `json:"finopt"` // Optional incoming services
	Finopt_arr []string
	Finerr     string `json:"finerr"` // Incoming error handling services
	Finerr_arr []string

	Foutman     string `json:"foutman"` // Mandatory outgoing services
	Foutman_arr []string
	Foutopt     string `json:"foutopt"` // Optional outgoing services
	Foutopt_arr []string
	Fouterr     string `json:"fouterr"` // Outgoing error handling services
	Fouterr_arr []string

	StaticDir  string       `json:"staticdir"` //Static files directory
	FileServer http.Handler //File server handler for static content

	Stream bool `json:"stream"` // File streaming mode - e.g. file download handler
}

//Route information structure for Handles with Regexp path
type route struct {
	pattern *regexp.Regexp
	handler http.Handler
}

//Custom handler to handle regexp and simple URLs
//Simple URLs are stored in urlMap and http handler for them are stored in defaultHandler[]
//If URL contains regexp, then regexpRoutes array is used which contains compiled pattern and handler
type RegexpHandler struct {
	regexpRoutes   []*route
	urlMap         map[string]ServiceMap
	defaultHandler map[string]http.Handler
}

var M_port int = atmi.FAIL
var M_ip string

//map the atmi error code (numbers + *) to some http error
//We shall provide default mappings.

var M_defaults ServiceMap

/* TLS Settings: */
var M_tls_enable int16 = FALSE
var M_tls_cert_file string
var M_tls_key_file string

//Conversion types
var M_convs = map[string]int{

	"json2ubf":  CONV_JSON2UBF,
	"text":      CONV_TEXT,
	"json":      CONV_JSON,
	"raw":       CONV_RAW,
	"json2view": CONV_JSON2VIEW,
	"static":    CONV_STATIC,
	"ext":       CONV_EXT,
}

var M_workers int
var M_ac *atmi.ATMICtx //Mainly shared for logging....

/*
 * Handler object, provides:
 * - ServeHTTP() for request handling (real time):
 * - HandleFunc() config time register routes to service with regexp masks.
 *   registers handler funcs/callbacks into RegexpHandler.defaultHandler or
 *   RegexpHandler.regexpRoutes + regexp
 *   which later are used by real time ServeHTTP()  to resolve services/urls...
 */
var M_handler RegexpHandler //Global HTTP call handler which contains regexp and simple handlers

var M_cctag string //CCTAG from env

//HandleFunc Can be used to add regexp or exact match URLs which uses dispathRequest()
// to handle request
//if regexp patters is nil, then add exact match URL, otherwise add compiled regexp
//and handler to global handler struct
func (h *RegexpHandler) HandleFunc(pattern *regexp.Regexp, svc ServiceMap) {
	if svc.Format == "regexp" || svc.Format == "r" {
		h.regexpRoutes = append(h.regexpRoutes, &route{pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			if CONV_STATIC == svc.Conv_int {
				result := strings.Split(r.URL.Path, "/")
				//M_ac.TpLogInfo("Got Static request... [%s] base: [%s] rex", r.URL.Path, result[1])
				http.StripPrefix("/"+result[1], svc.FileServer).ServeHTTP(w, r)

			} else {
				//M_ac.TpLogInfo("Got XATMI request...")
				dispatchRequest(w, r, svc)
			}
		})})
	} else {
		h.urlMap[svc.Url] = svc
		h.defaultHandler[svc.Url] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if CONV_STATIC == svc.Conv_int {
				result := strings.Split(r.URL.Path, "/")
				//M_ac.TpLogInfo("Got Static request... [%s] base: [%s] stat", r.URL.Path, result[1])
				http.StripPrefix("/"+result[1], svc.FileServer).ServeHTTP(w, r)
			} else {
				//M_ac.TpLogInfo("Got XATMI request...")
				dispatchRequest(w, r, svc)
			}
		})
	}
}

//ServeHTTP function to satisfy http.Handler interface
//This function is called when incomming request is received
//It checks if urlMap contains exact match URL and if it does, calls corresponding
// handler which calls dispatchRequest()
//If URL is not in urlMap (exact match) ServeHTTP checks all compiled regexps
//and calls dispatchRequest() on match.
func (h *RegexpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	//M_ac.TpLogInfo("ServeHTTP: [%s]", r.URL.Path)

	svc := h.urlMap[r.URL.Path]
	if svc.Svc != "" || svc.Echo {
		//M_ac.TpLogInfo("Default ServeHTTP: [%s]", r.URL.Path)

		h.defaultHandler[r.URL.Path].ServeHTTP(w, r)
		return
	}

	for _, route := range h.regexpRoutes {
		//M_ac.TpLogInfo("REX ServeHTTP: [%s]", r.URL.Path)
		if route.pattern.MatchString(r.URL.Path) {
			route.handler.ServeHTTP(w, r)
			return
		}
	}
	//M_ac.TpLogInfo("404 ServeHTTP: [%s]", r.URL.Path)

	// no pattern matched; send 404 response
	http.NotFound(w, r)
}

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
	case "json2ubf":
		svc.Errors_int = ERRORS_JSON2UBF
		break
	case "json2view":
		svc.Errors_int = ERRORS_JSON2VIEW
		break
	case "text":
		svc.Errors_int = ERRORS_TEXT
		break
	case "ext":
		svc.Errors_int = ERRORS_EXT
		break
	default:
		return fmt.Errorf("Unsupported error type [%s]", svc.Errors)
	}

	return nil
}

//Run the listener
//Listener uses custom handler to support Regexp and simple URLs separatly
func apprun(ac *atmi.ATMICtx) error {

	var err error
	//TODO: Some works needed for TLS...
	listenOn := fmt.Sprintf("%s:%d", M_ip, M_port)
	ac.TpLog(atmi.LOG_INFO, "About to listen on: (ip: %s, port: %d) %s",
		M_ip, M_port, listenOn)

	/*
		l, err := net.Listen("tcp", listenOn)

		if err != nil {
			ac.TpLog(atmi.LOG_ERROR, "Listen failed on %s: %v", listenOn, err)
			return err
		}

		defer l.Close()

		l = netutil.LimitListener(l, M_workers)
	*/

	if TRUE == M_tls_enable {

		/* To prepare cert (self-signed) do following steps:
		 * - TODO
		 */
		err = http.ListenAndServeTLS(listenOn, M_tls_cert_file, M_tls_key_file, &M_handler)

		/*	err = http.ServeTLS(l, &M_handler, M_tls_cert_file, M_tls_key_file) */

		ac.TpLog(atmi.LOG_ERROR, "ListenAndServeTLS() failed: %s", err)
	} else {
		/*err = http.Serve(l, &M_handler)*/
		err = http.ListenAndServe(listenOn, &M_handler)
		ac.TpLog(atmi.LOG_ERROR, "ListenAndServe() failed: %s", err)
	}

	return err
}

//Init function, read config (with CCTAG)
func dispatchRequest(w http.ResponseWriter, req *http.Request, svc ServiceMap) {

	M_ac.TpLog(atmi.LOG_DEBUG, "URL [%s] getting free goroutine caller: %s",
		req.URL, req.RemoteAddr)

	nr := <-M_freechan

	M_ac.TpLogInfo("Got free goroutine, nr %d", nr)

	handleMessage(M_ctxs[nr], &svc, w, req)

	M_ac.TpLogInfo("Request processing done %d... releasing the context", nr)

	M_freechan <- nr

}

//Map the ATMI Errors to Http errors
//Format: <atmi_err>:<http_err>,<*>:<http_err>
//* - means any other unmapped ATMI error
//@param svc	Service map
func parseHTTPErrorMap(ac *atmi.ATMICtx, svc *ServiceMap) error {

	svc.Errors_fmt_http_map = make(map[string]int)
	ac.TpLogDebug("Splitting error mapping string [%s]",
		svc.Errors_fmt_http_map_str)

	parsed := regexp.MustCompile(", *").Split(svc.Errors_fmt_http_map_str, -1)

	for index, element := range parsed {
		ac.TpLogDebug("Got pair [%s] at %d", element, index)

		pair := regexp.MustCompile(": *").Split(element, -1)

		pairLen := len(pair)

		if pairLen < 2 || pairLen > 2 {
			ac.TpLogError("Invalid http error pair: [%s] "+
				"parsed into %d elms", element, pairLen)

			return fmt.Errorf("Invalid http error pair: [%s] "+
				"parsed into %d elms", element, pairLen)
		}

		number, err := strconv.ParseInt(pair[1], 10, 0)

		if err != nil {
			ac.TpLogError("Failed to parse http error code %s (%s)",
				pair[1], err)
			return fmt.Errorf("Failed to parse http error code %s (%s)",
				pair[1], err)
		}

		//Add to hash
		svc.Errors_fmt_http_map[pair[0]] = int(number)
	}

	return nil
}

//Print the summary of the service after init
func printSvcSummary(ac *atmi.ATMICtx, svc *ServiceMap) {
	ac.TpLogWarn("Service: %s, Url: %s, Async mode: %t, Log request svc: [%s], "+
		"Errors:%d (%s), Async echo %t, "+
		"Streaming mode: %t, "+
		"Filters: inman:%s/inopt:%s/inerr:%s/outman:%s/outopt:%s/outerr:%s",
		svc.Svc,
		svc.Url,
		svc.Asynccall,
		svc.Reqlogsvc,
		svc.Errors_int,
		svc.Errors,
		svc.Asyncecho,
		svc.Stream,
		svc.Finman, svc.Finopt, svc.Finerr, svc.Foutman, svc.Foutopt, svc.Fouterr)

	ac.TpLogWarn("fileupload:%t tempdir:[%s]", svc.Fileupload, svc.Tempdir)
}

//Validate external service definitions
//Also perform any needed parsings before we open the service
func validateExtService(ac *atmi.ATMICtx, svc *ServiceMap) error {

	//check that errors are correct
	if svc.Conv_int == CONV_EXT {

		if svc.Errors_int != ERRORS_EXT {
			ac.TpLogError("Service [%s] conv is 'ext', but errors not 'ext' [%s]!",
				svc.Svc, svc.Errors)

			return errors.New(fmt.Sprintf("Service [%s] conv is 'ext', but errors not ext '%s'!",
				svc.Svc, svc.Errors))
		}
	} else {
		//Others shall not use ext error mode
		if svc.Errors_int == ERRORS_EXT {
			ac.TpLogError("Service [%s] conv is not '%s', but errors is 'ext'!",
				svc.Svc, svc.Conv)

			return errors.New(fmt.Sprintf("Service [%s] conv is not '%s', but errors is 'ext'!",
				svc.Svc, svc.Conv))
		}
	}

	//Trim off whitespace
	svc.Finman = strings.TrimSpace(svc.Finman)
	svc.Finopt = strings.TrimSpace(svc.Finopt)

	if svc.Conv_int == CONV_EXT {

		svc.Finerr = strings.TrimSpace(svc.Finerr)

		svc.Foutman = strings.TrimSpace(svc.Foutman)
		svc.Foutopt = strings.TrimSpace(svc.Foutopt)
		svc.Fouterr = strings.TrimSpace(svc.Fouterr)

		//Split by comma

		if "" != svc.Finman {
			svc.Finman_arr = strings.Split(svc.Finman, ",")
		}
		if "" != svc.Finopt {
			svc.Finopt_arr = strings.Split(svc.Finopt, ",")
		}
		if "" != svc.Finerr {
			svc.Finerr_arr = strings.Split(svc.Finerr, ",")
		}

		if "" != svc.Foutman {
			svc.Foutman_arr = strings.Split(svc.Foutman, ",")
		}
		if "" != svc.Foutopt {
			svc.Foutopt_arr = strings.Split(svc.Foutopt, ",")
		}
		if "" != svc.Fouterr {
			svc.Fouterr_arr = strings.Split(svc.Fouterr, ",")
		}
	} else {

		if "" != svc.Finerr {
			return errors.New(fmt.Sprintf("`finerr' not suitable for conv %s",
				svc.Conv))
		}

		if "" != svc.Foutman {
			return errors.New(fmt.Sprintf("`foutman' not suitable for conv %s",
				svc.Conv))
		}
		if "" != svc.Foutopt {
			return errors.New(fmt.Sprintf("`foutopt' not suitable for conv %s",
				svc.Conv))
		}
		if "" != svc.Fouterr {
			return errors.New(fmt.Sprintf("`fouterr' not suitable for conv %s",
				svc.Conv))
		}

		if svc.Fileupload {
			return errors.New(fmt.Sprintf("`fileupload' is valid only for ext conv (cur %s)",
				svc.Conv))
		}

		if svc.Parseform {
			return errors.New(fmt.Sprintf("`parseform' is valid only for ext conv (cur %s",
				svc.Conv))
		}

	}

	if svc.Fileupload && svc.Parseform {
		return errors.New(fmt.Sprintf("`fileupload' or `parseform' must be used exclusively"))
	}

	return nil
}

//Un-init function
func appinit(ac *atmi.ATMICtx) error {
	//runtime.LockOSThread()
	M_handler.urlMap = make(map[string]ServiceMap)
	M_handler.defaultHandler = make(map[string]http.Handler)

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
	M_defaults.Errfmt_view_onsucc = ERRFMT_VIEW_ONSUCC_DEFAULT
	M_defaults.Stream = STREAM_DEFAULT

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
	M_cctag = os.Getenv("NDRX_CCTAG")
	buf.BChg(u.EX_CC_LOOKUPSECTION, 0, fmt.Sprintf("%s/%s", progsection, M_cctag))

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
		fldName, err := buf.BGetString(u.EX_CC_KEY, occ)

		if nil != err {
			ac.TpLog(atmi.LOG_ERROR, "Failed to get field "+
				"%d occ %d", u.EX_CC_KEY, occ)
			return errors.New(err.Error())
		}

		ac.TpLog(atmi.LOG_DEBUG, "Got config field [%s]", fldName)

		switch fldName {
		case "debug":
			//Set debug configuration string
			debug, _ := buf.BGetString(u.EX_CC_VALUE, occ)
			ac.TpLogDebug("Got [%s] = [%s] ", fldName, debug)
			if err := ac.TpLogConfig((atmi.LOG_FACILITY_NDRX | atmi.LOG_FACILITY_UBF | atmi.LOG_FACILITY_TP),
				-1, debug, "ROUT", ""); nil != err {
				ac.TpLogError("Invalid debug config [%s] %d:[%s]",
					debug, err.Code(), err.Message())
				return fmt.Errorf("Invalid debug config [%s] %d:[%s]",
					debug, err.Code(), err.Message())
			}

			break
		case "workers":
			M_workers, _ = buf.BGetInt(u.EX_CC_VALUE, occ)
			break
		case "gencore":
			gencore, _ := buf.BGetInt(u.EX_CC_VALUE, occ)

			if TRUE == gencore {
				//Process signals by default handlers
				ac.TpLogInfo("gencore=1 - SIGSEG signal will be " +
					"processed by default OS handler")
				// Have some core dumps...
				C.signal(11, nil)
			}
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
			jsonDefault, _ := buf.BGetByteArr(u.EX_CC_VALUE, occ)

			jerr := json.Unmarshal(jsonDefault, &M_defaults)
			if jerr != nil {
				ac.TpLog(atmi.LOG_ERROR,
					fmt.Sprintf("Failed to parse defaults: %s", jerr))
				return jerr
			}

			if M_defaults.Errors_fmt_http_map_str != "" {
				if jerr := parseHTTPErrorMap(ac, &M_defaults); err != nil {
					return jerr
				}
			}

			remapErrors(&M_defaults)

			M_defaults.Conv_int = M_convs[M_defaults.Conv]
			if M_defaults.Conv_int == 0 {
				return fmt.Errorf("Invalid conv: %s", M_defaults.Conv)
			}

			//Validate view settings (if any)
			if errS := VIEWSvcValidateSettings(ac, &M_defaults); errS != nil {
				return errS
			}

			//Validate ext
			if errS := validateExtService(ac, &M_defaults); errS != nil {
				return errS
			}

			printSvcSummary(ac, &M_defaults)

			break
		}
	}

	//Bug #461 Load the services in second pass..
	ac.TpLogInfo("Second pass config process - service load")
	for occ := 0; occ < occs; occ++ {
		ac.TpLog(atmi.LOG_DEBUG, "occ %d", occ)
		fldName, err := buf.BGetString(u.EX_CC_KEY, occ)

		if nil != err {
			ac.TpLog(atmi.LOG_ERROR, "Failed to get field "+
				"%d occ %d", u.EX_CC_KEY, occ)
			return errors.New(err.Error())
		}

		ac.TpLog(atmi.LOG_DEBUG, "Got config field [%s]", fldName)

		//Load routes...
		if strings.HasPrefix(fldName, "/") {
			cfgVal, _ := buf.BGetString(u.EX_CC_VALUE, occ)

			ac.TpLogInfo("Got route config [%s]", cfgVal)

			tmp := M_defaults

			//Override the stuff from current config

			//err := json.Unmarshal(cfgVal, &tmp)
			decoder := json.NewDecoder(strings.NewReader(cfgVal))
			//conf := Config{}
			err := decoder.Decode(&tmp)

			if err != nil {
				ac.TpLog(atmi.LOG_ERROR,
					fmt.Sprintf("Failed to parse config key %s: %s",
						fldName, err))
				return err
			}

			ac.TpLogDebug("Got route: URL [%s] -> Service [%s]",
				fldName, tmp.Svc)
			tmp.Url = fldName

			//Parse http errors for
			if tmp.Errors_fmt_http_map_str != "" {
				if jerr := parseHTTPErrorMap(ac, &tmp); err != nil {
					return jerr
				}
			}

			remapErrors(&tmp)
			//Map the conv
			tmp.Conv_int = M_convs[tmp.Conv]

			if tmp.Conv_int == 0 {
				return fmt.Errorf("Invalid conv: %s", tmp.Conv)

			} else if CONV_STATIC == tmp.Conv_int {

				//Check that it is directory and we can read it
				info, err := os.Stat(tmp.StaticDir)
				if err != nil {
					return fmt.Errorf("Failed to stat [%s] directoy - does it exists?",
						tmp.StaticDir)
				}

				if !info.IsDir() {
					return fmt.Errorf("Path [%s] is NOT a directoy! Cannot server files",
						tmp.StaticDir)
				}

				tmp.FileServer = http.FileServer(http.Dir(tmp.StaticDir))

				if nil == tmp.FileServer {
					return fmt.Errorf("Failed to create static file server "+
						"for [%s] directory",
						tmp.StaticDir)
				} else {
					ac.TpLogInfo("Static file server [%s] OK", tmp.StaticDir)
				}

			}

			//Default temporary folder
			if "" == tmp.Tempdir {
				tmp.Tempdir = os.TempDir()
			}

			//Validate view settings (if any)
			if err = VIEWSvcValidateSettings(ac, &tmp); err != nil {
				return err
			}

			//Validate ext
			if err = validateExtService(ac, &tmp); err != nil {
				return err
			}

			printSvcSummary(ac, &tmp)

			ac.TpLogInfo("Checking if service uses regexp")
			//Add to HTTP listener
			if tmp.Format == "regexp" || tmp.Format == "r" {
				if r, err := regexp.Compile(fldName); err == nil {
					ac.TpLogInfo("Regexp compiled")
					M_handler.HandleFunc(r, tmp)
				} else {
					ac.TpLogError("Failed to compile regexp [%s]",
						err.Error())
				}
			} else {
				M_handler.HandleFunc(nil, tmp)
			}
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

	if M_defaults.Parsecookies && !M_defaults.Parseheaders {
		return errors.New("Invalid config: parsecookies works only in parseheader mode")
	}

	//Add the default erorr mappings
	if M_defaults.Errors_fmt_http_map_str == "" {

		//https://golang.org/src/net/http/status.go
		M_defaults.Errors_fmt_http_map = make(map[string]int)
		//Accepted
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPMINVAL)] =
			http.StatusOK
		//Errors:
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEABORT)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEBADDESC)] =
			http.StatusBadRequest
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEBLOCK)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEINVAL)] =
			http.StatusBadRequest
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPELIMIT)] =
			http.StatusRequestEntityTooLarge
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPENOENT)] =
			http.StatusNotFound
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEOS)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEPERM)] =
			http.StatusUnauthorized
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEPROTO)] =
			http.StatusBadRequest
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPESVCERR)] =
			http.StatusBadGateway
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPESVCFAIL)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPESYSTEM)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPETIME)] =
			http.StatusGatewayTimeout
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPETRAN)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPERMERR)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEITYPE)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEOTYPE)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPERELEASE)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEHAZARD)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEHEURISTIC)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEEVENT)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEMATCH)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEDIAGNOSTIC)] =
			http.StatusInternalServerError
		M_defaults.Errors_fmt_http_map[strconv.Itoa(atmi.TPEMIB)] =
			http.StatusInternalServerError
		//Anything other goes to server error.
		M_defaults.Errors_fmt_http_map["*"] = http.StatusInternalServerError

	}

	ac.TpLogInfo("About to init woker pool, number of workers: %d", M_workers)

	initPool(ac)

	return nil
}

//Un-init & Terminate the application
func unInit(ac *atmi.ATMICtx, retCode int) {

	for i := 0; i < M_workers; i++ {
		nr := <-M_freechan

		ac.TpLogWarn("Terminating %d context", nr)
		M_ctxs[nr].TpTerm()
		M_ctxs[nr].FreeATMICtx()
	}

	ac.TpTerm()
	ac.FreeATMICtx()
	os.Exit(retCode)
}

//Handle the shutdown
func handleShutdown(ac *atmi.ATMICtx) {
	signalChannel := make(chan os.Signal, 2)
	signal.Notify(signalChannel, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-signalChannel
		//Shutdown all contexts...
		ac.TpLogWarn("Got signal %d - shutting down all XATMI client contexts",
			sig)
		unInit(ac, atmi.SUCCEED)
	}()
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
		M_ac.TpLogError("Failed to init: %s", err)
		os.Exit(atmi.FAIL)
	}

	handleShutdown(M_ac)

	M_ac.TpLogWarn("REST Incoming init ok - serving...")

	if err := apprun(M_ac); nil != err {
		unInit(M_ac, atmi.FAIL)
	}

	unInit(M_ac, atmi.SUCCEED)
}

/* vim: set ts=4 sw=4 et smartindent: */
