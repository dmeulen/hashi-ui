package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/jippi/hashi-ui/backend/config"
	"github.com/jippi/hashi-ui/backend/consul"
	"github.com/jippi/hashi-ui/backend/nomad"
	"github.com/newrelic/go-agent"
	"github.com/op/go-logging"
)

var logger = logging.MustGetLogger("hashi-ui")

func startLogging(logLevel string) {
	logBackend := logging.NewLogBackend(os.Stderr, "", 0)

	format := logging.MustStringFormatter(
		`%{color}%{time:15:04:05.000} %{shortfile} ▶ %{level:.5s} %{color:reset} %{message}`,
	)
	logBackendFormatted := logging.NewBackendFormatter(logBackend, format)

	logBackendFormattedAndLeveled := logging.AddModuleLevel(logBackendFormatted)

	realLogLevel, err := logging.LogLevel(strings.ToUpper(logLevel))
	if err != nil {
		fmt.Printf("%s (%s)", err, logLevel)
		os.Exit(1)
	}

	logBackendFormattedAndLeveled.SetLevel(realLogLevel, "")

	logging.SetBackend(logBackendFormattedAndLeveled)
}

func main() {
	cfg := config.DefaultConfig()
	cfg.Parse()

	newrelicConfig := newrelic.NewConfig(cfg.NewRelicAppName, cfg.NewRelicLicense)
	newrelicConfig.Logger = newrelic.NewLogger(os.Stdout)

	if cfg.NewRelicAppName == "" || cfg.NewRelicLicense == "" {
		newrelicConfig.Enabled = false
	}

	_, err := newrelic.NewApplication(newrelicConfig)
	if err != nil {
		logger.Error(err)
		os.Exit(1)
	}

	startLogging(cfg.LogLevel)

	logger.Infof("-----------------------------------------------------------------------------")
	logger.Infof("|                             HASHI UI                                      |")
	logger.Infof("-----------------------------------------------------------------------------")
	if !cfg.HttpsEnable {
		logger.Infof("| listen-address        : http://%-43s |", cfg.ListenAddress)
	} else {
		logger.Infof("| listen-address      : https://%-43s  |", cfg.ListenAddress)
	}
	logger.Infof("| server-certificate   	: %-50s |", cfg.ServerCert)
	logger.Infof("| server-key       	: %-50s |", cfg.ServerKey)
	logger.Infof("| proxy-address   	: %-50s |", cfg.ProxyAddress)
	logger.Infof("| log-level       	: %-50s |", cfg.LogLevel)

	if cfg.NewRelicAppName != "" && cfg.NewRelicLicense != "" {
		logger.Infof("| newrelic-app-name   : %-50s |", cfg.NewRelicAppName)
		logger.Infof("| newrelic-license    : %-50s |", strings.Repeat("*", len(cfg.NewRelicLicense)))
	}

	// Nomad
	logger.Infof("| nomad-enable     	: %-50t |", cfg.NomadEnable)
	if cfg.NomadReadOnly {
		logger.Infof("| nomad-read-only       : %-50s |", "Yes")
	} else {
		logger.Infof("| nomad-read-only       : %-50s |", "No (Hashi-UI can change Nomad state)")
	}
	logger.Infof("| nomad-address         : %-50s |", cfg.NomadAddress)
	logger.Infof("| nomad-ca-cert         : %-50s |", cfg.NomadCACert)
	logger.Infof("| nomad-client-cert     : %-50s |", cfg.NomadClientCert)
	logger.Infof("| nomad-client-key      : %-50s |", cfg.NomadClientKey)
	logger.Infof("| nomad-skip-verify   	: %-50t |", cfg.NomadSkipVerify)
	logger.Infof("| hide-env-data       	: %-50v |", cfg.NomadHideEnvData)
	if cfg.NomadSkipVerify {
		logger.Infof("| nomad-skip-verify     : %-50s |", "Yes")
	} else {
		logger.Infof("| nomad-skip-verify     : %-50s |", "No")
	}

	// Consul
	logger.Infof("| consul-enable     	: %-50t |", cfg.ConsulEnable)
	if cfg.ConsulReadOnly {
		logger.Infof("| consul-read-only     : %-50s |", "Yes")
	} else {
		logger.Infof("| consul-read-only     : %-50s |", "No (Hashi-UI can change Consul state)")
	}
	logger.Infof("| consul-address       : %-50s |", cfg.ConsulAddress)
	logger.Infof("| consul.acl-token     : %-50s |", cfg.ConsulACLToken)

	logger.Infof("-----------------------------------------------------------------------------")
	logger.Infof("")

	if !cfg.NomadEnable && !cfg.ConsulEnable {
		logger.Fatal("Please enable at least Consul (--consul-enable) or Nomad (--nomad-enable)")
	}

	myAssetFS := assetFS()
	router := mux.NewRouter()

	if cfg.NomadEnable {
		nomadHub, nomadSuccess := nomad.Initialize(cfg)
		if !nomadSuccess {
			logger.Fatalf("Failed to start Nomad hub, please check your configuration")
		}
		logger.Infof("Nomad client successfully initialized")

		router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			logger.Infof("Redirecting / to /nomad")
			w.Write([]byte("<script>document.location.href='" + cfg.ProxyAddress + "/nomad'</script>"))
			return
		})

		router.HandleFunc("/ws/nomad", nomadHub.Handler)
		router.HandleFunc("/ws/nomad/{region}", nomadHub.Handler)
		router.HandleFunc("/nomad/{region}/download/{path:.*}", nomadHub.DownloadFile)
	}

	if cfg.ConsulEnable {
		consulHub, consulSuccess := consul.Initialize(cfg)
		if !consulSuccess {
			logger.Fatalf("Failed to start Consul hub, please check your configuration")
		}

		if !cfg.NomadEnable {
			router.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				logger.Infof("Redirecting / to /consul")
				http.Redirect(w, r, cfg.ProxyAddress+"/consul", 302)
			})
		}

		logger.Infof("Consul client successfully initialized")
		router.HandleFunc("/ws/consul", consulHub.Handler)
		router.HandleFunc("/ws/consul/{region}", consulHub.Handler)
	}

	router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		responseFile := "/index.html"

		if idx := strings.Index(r.URL.Path, "static/"); idx != -1 {
			responseFile = r.URL.Path[idx:]
		}

		if idx := strings.Index(r.URL.Path, "favicon.png"); idx != -1 {
			responseFile = "/favicon.png"
		}

		if idx := strings.Index(r.URL.Path, "config.js"); idx != -1 {
			response := make([]string, 0)
			response = append(response, fmt.Sprintf("window.CONSUL_ENABLED=%s", strconv.FormatBool(cfg.ConsulEnable)))
			response = append(response, fmt.Sprintf("window.CONSUL_READ_ONLY=%s", strconv.FormatBool(cfg.ConsulReadOnly)))

			response = append(response, fmt.Sprintf("window.NOMAD_ENABLED=%s", strconv.FormatBool(cfg.NomadEnable)))
			response = append(response, fmt.Sprintf("window.NOMAD_READ_ONLY=%s", strconv.FormatBool(cfg.NomadReadOnly)))

			enabledServices := make([]string, 0)
			if cfg.ConsulEnable {
				enabledServices = append(enabledServices, "'consul'")
			}
			if cfg.NomadEnable {
				enabledServices = append(enabledServices, "'nomad'")
			}

			response = append(response, fmt.Sprintf("window.ENABLED_SERVICES=[%s]", strings.Join(enabledServices, ",")))

			var endpointURL string
			if cfg.ProxyAddress != "" {
				endpointURL = fmt.Sprintf("\"%s\"", strings.TrimSuffix(cfg.ProxyAddress, "/"))
			} else {
				endpointURL = "document.location.protocol + '//' + document.location.hostname + ':' + (window.NOMAD_ENDPOINT_PORT || document.location.port)"
			}

			response = append(response, fmt.Sprintf("window.NOMAD_ENDPOINT=%s", endpointURL))

			w.Header().Set("Content-Type", "application/javascript")
			w.Write([]byte(strings.Join(response, "\n")))
			return
		}

		if bs, assetErr := myAssetFS.Open(responseFile); err != nil {
			logger.Errorf("%s: %s", responseFile, assetErr)
		} else {
			stat, err := bs.Stat()
			if err != nil {
				logger.Errorf("Failed to stat %s: %s", responseFile, err)
			} else {
				http.ServeContent(w, r, responseFile[1:], stat.ModTime(), bs)
			}
		}
	})

	logger.Infof("Listening ...")
	if cfg.HttpsEnable {
		if cfg.ServerCert == "" || cfg.ServerKey == "" {
			logger.Fatal("Using https protocol but server certificate or key were not specified.")
		}
		err = http.ListenAndServeTLS(cfg.ListenAddress, cfg.ServerCert, cfg.ServerKey, router)
	} else {
		err = http.ListenAndServe(cfg.ListenAddress, router)
	}
	if err != nil {
		logger.Fatal(err)
	}
}
