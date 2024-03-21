package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/urfave/cli/v2"
	"gopkg.in/yaml.v2"
)

type configuration struct {
	TestLength       string    `yaml:"test-length"`
	TestInterval     string    `yaml:"test-interval"`
	ServerPort       string    `yaml:"server-port"`
	TsdbServer       string    `yaml:"grafana-address"`
	TsdbPort         string    `yaml:"grafana-port"`
	InfluxURL        string    `yaml:"influx-url"`
	TsdbDownPrefix   string    `yaml:"tsdb-download-prefix"`
	TsdbUpPrefix     string    `yaml:"tsdb-upload-prefix"`
	PerfServers      []servers `yaml:"iperf-servers"`
	MeasurementName  string    `yaml:"measurement-name"`
	GraphiteHostPort string
	TsdbHostPort     string
	Hostname         string
}

type servers map[string]string

const (
	netperfTCP         = "TCP_STREAM"
	netperfUDP         = "UDP_STREAM"
	defaultNetperfRepo = "quay.io/networkstatic/netperf"
	defaultIperfRepo   = "quay.io/networkstatic/iperf3"
	defaultIperfPort   = "5201"
	defaultNetperfPort = "12865"
	defaultCarbonPort  = "2003"
)

var log = logrus.New()

var (
	cliFlags          flags
	configFilePresent = true
	iperfBinary       string
	netperfBinary     string
)

type flags struct {
	configPath     string
	imageRepo      string
	perfServers    string
	tsdbType       string
	grafanaServer  string
	grafanaPort    string
	influxURL      string
	testInterval   string
	testLength     string
	parallelConn   string
	perfServerPort string
	downloadPrefix string
	uploadPrefix   string
	kentikEmail    string
	kentikToken    string
	netperf        bool
	noContainer    bool
	debug          bool
}

func main() {
	// instantiate the cli
	app := cli.NewApp()
	// flags are stored in the global flags variable
	app = &cli.App{
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "configuration",
				Value:       "configuration.yaml",
				Usage:       "Path to the configuration file - example: -configuration=path/configuration.yaml",
				Destination: &cliFlags.configPath,
				EnvVars:     []string{"CBANDWIDTH_CONFIG"},
			},
			&cli.StringFlag{
				Name:        "image",
				Value:       defaultIperfRepo,
				Usage:       "Custom repo to an Iperf3 image",
				Destination: &cliFlags.imageRepo,
				EnvVars:     []string{"CBANDWIDTH_PERF_IMAGE"},
			},
			&cli.StringFlag{
				Name:        "perf-servers",
				Value:       "",
				Usage:       "remote host and IP address of the perf server destination(s) seperated by a \":\" if multiple values, can be a host:ip pair or just an address ex. --remote-hosts=192.168.1.100,host2:172.16.100.20",
				Destination: &cliFlags.perfServers,
				EnvVars:     []string{"CBANDWIDTH_PERF_SERVERS"},
			},
			&cli.StringFlag{
				Name:        "tsdbtype",
				Value:       "",
				Usage:       "type of tsdb to use. accepts 'influx' as input to override default grafana outputs",
				Destination: &cliFlags.tsdbType,
				EnvVars:     []string{"CBANDWIDTH_TSDB_TYPE"},
			},
			&cli.StringFlag{
				Name:        "grafana-address",
				Value:       "",
				Usage:       "address of the grafana/carbon server",
				Destination: &cliFlags.grafanaServer,
				EnvVars:     []string{"CBANDWIDTH_GRAFANA_ADDRESS"},
			},
			&cli.StringFlag{
				Name:        "grafana-port",
				Value:       defaultCarbonPort,
				Usage:       "address of the grafana/carbon port",
				Destination: &cliFlags.grafanaPort,
				EnvVars:     []string{"CBANDWIDTH_GRAFANA_PORT"},
			},
			&cli.StringFlag{
				Name:        "influx-url",
				Value:       "",
				Usage:       "address of the influx server",
				Destination: &cliFlags.influxURL,
				EnvVars:     []string{"CBANDWIDTH_INFLUX_ADDRESS"},
			},
			&cli.StringFlag{
				Name:        "test-interval",
				Value:       "300",
				Usage:       "the time in seconds between performance polls",
				Destination: &cliFlags.testInterval,
				EnvVars:     []string{"CBANDWIDTH_POLL_INTERVAL"},
			},
			&cli.StringFlag{
				Name:        "test-length",
				Value:       "5",
				Usage:       "the length of time the perf test run for in seconds",
				Destination: &cliFlags.testLength,
				EnvVars:     []string{"CBANDWIDTH_POLL_LENGTH"},
			},
			&cli.StringFlag{
				Name:        "parallel-connections",
				Value:       "1",
				Usage:       "Iperf only, number of simultaneous Iperf connections to make to the server",
				Destination: &cliFlags.parallelConn,
				EnvVars:     []string{"CBANDWIDTH_IPERF_PARALLEL"},
			},
			&cli.StringFlag{
				Name:        "perf-server-port",
				Value:       defaultIperfPort,
				Usage:       "iperf server port (iperf default is 5201 and netperf default is 12865",
				Destination: &cliFlags.perfServerPort,
				EnvVars:     []string{"CBANDWIDTH_PERF_SERVER_PORT"},
			},
			&cli.StringFlag{
				Name:        "tsdb-download-prefix",
				Value:       "bandwidth.download",
				Usage:       "the download prefix of the stored tsdb data in graphite",
				Destination: &cliFlags.downloadPrefix,
				EnvVars:     []string{"CBANDWIDTH_DOWNLOAD_PREFIX"},
			},
			&cli.StringFlag{
				Name:        "tsdb-upload-prefix",
				Value:       "bandwidth.upload",
				Usage:       "the upload prefix of the stored tsdb data in graphite, not applicable for netperf",
				Destination: &cliFlags.uploadPrefix,
				EnvVars:     []string{"CBANDWIDTH_UPLOAD_PREFIX"},
			},
			&cli.StringFlag{
				Name:        "kentik-email",
				Value:       "",
				Usage:       "email address used for Kentik Portal login",
				Destination: &cliFlags.kentikEmail,
				EnvVars:     []string{"CBANDWIDTH_KENTIK_EMAIL"},
			},
			&cli.StringFlag{
				Name:        "kentik-token",
				Value:       "",
				Usage:       "API token used for Kentik Portal login",
				Destination: &cliFlags.kentikToken,
				EnvVars:     []string{"CBANDWIDTH_KENTIK_TOKEN"},
			},
			&cli.BoolFlag{
				Name:        "netperf",
				Value:       false,
				Usage:       "use netperf and netserver instead of iperf",
				Destination: &cliFlags.netperf,
				EnvVars:     []string{"CBANDWIDTH_NETPERF"},
			},
			&cli.BoolFlag{
				Name:        "nocontainer",
				Value:       false,
				Usage:       "Do not use docker or podman and run the iperf3 binary by the host - default is containerized",
				Destination: &cliFlags.noContainer,
				EnvVars:     []string{"CBANDWIDTH_NOCONTAINER"},
			},
			&cli.BoolFlag{
				Name:        "debug",
				Value:       false,
				Usage:       "Run in debug mode to display all shell commands being executed",
				Destination: &cliFlags.debug,
				EnvVars:     []string{"CBANDWIDTH_DEBUG"},
			},
		},
	}

	app.Name = "cloud-bandwidth"
	app.Usage = "measure endpoint bandwidth and record the results to a tsdb"
	app.Before = func(c *cli.Context) error {
		return nil
	}
	app.Action = func(c *cli.Context) error {
		// call the applications function
		runApp()
		return nil
	}
	app.Run(os.Args)
}

// runApp parses the configuration and runs the tests
func runApp() {
	logrus.SetLevel(logrus.DebugLevel)
	logrus.SetFormatter(&logrus.TextFormatter{})

	if cliFlags.debug {
		log.Level = logrus.DebugLevel
	}

	// read in the yaml configuration from configuration.yaml
	configFileData, err := os.ReadFile(cliFlags.configPath)
	if err != nil {
		log.Info("no configuration file found, defaulting to command line arguments")
		configFilePresent = false
	}

	config := configuration{}
	// read in the configuration file if one exists
	if configFilePresent {
		if err := yaml.Unmarshal([]byte(configFileData), &config); err != nil {
			log.Fatal(err)
		}
	}

	// check the configuration file first for the configuration files values, fallback to the CLI values otherwise
	if configFilePresent {
		// check for new flag influx to write out Influx format to external HTTP endpoint
		if cliFlags.tsdbType == "influx" {
			if cliFlags.influxURL != "" {
				// override the config file with cliflag
				config.InfluxURL = cliFlags.influxURL
			} else {
				if config.InfluxURL == "" {
					log.Fatal("tsdbType indicated as 'influx' but no Influx URL was passed")
				}
			}
			log.Errorf("Influx Selected : %s", config.InfluxURL)
		}
		if cliFlags.grafanaServer != "" {
			config.GraphiteHostPort = net.JoinHostPort(cliFlags.grafanaServer, cliFlags.grafanaPort)
		} else {
			if configFilePresent {
				config.GraphiteHostPort = net.JoinHostPort(config.TsdbServer, config.TsdbPort)
			} else {
				log.Fatal("no grafana/carbon server and/or port were passed")
			}
		}
		if config.TestInterval != "" {
			cliFlags.testInterval = config.TestInterval
		}
		if config.TestLength != "" {
			cliFlags.testLength = config.TestLength
		}
		if config.TsdbUpPrefix != "" {
			cliFlags.uploadPrefix = config.TsdbUpPrefix
		}
		if config.TsdbDownPrefix != "" {
			cliFlags.downloadPrefix = config.TsdbDownPrefix
		}
	}

	// assign the grafana server from the CLI
	if cliFlags.tsdbType != "influx" {
		if config.GraphiteHostPort == "" {
			if cliFlags.grafanaServer == "" {
				log.Warn("No Grafana server was passed to the app, tests will still run, but will not be able to write to a grafana server")
			} else {
				config.GraphiteHostPort = net.JoinHostPort(cliFlags.grafanaServer, cliFlags.grafanaPort)
			}
		}
	}
	// assign the influx server from the CLI
	if config.InfluxURL == "" {
		if cliFlags.influxURL == "" {
			log.Warn("No influx URL was passed to the app, tests will still run but will not be able to write to an influx endpoint")
		} else {
			config.InfluxURL = cliFlags.influxURL
		}
	}

	// merge the CLI with the configuration files if both exist
	if cliFlags.perfServers != "" {
		tunnelDestList := strings.Split(cliFlags.perfServers, ",")
		for _, tunnelDest := range tunnelDestList {
			perfServerMap := mapPerfDest(tunnelDest)
			config.PerfServers = append(config.PerfServers, perfServerMap)
		}
	}

	// get our hostname to add to reported measurements
	hostname, err := os.Hostname()
	if err != nil {
		log.Error(err)
	} else {
		config.Hostname = hostname
	}
	// Log configuration parameters for debugging
	log.Debug("Configuration as follows:")
	log.Debugf("Hostname = %s", hostname)
	log.Debugf("[Config] Grafana Server = %s", config.GraphiteHostPort)
	log.Debugf("[Config] Influx URL = %s", config.InfluxURL)
	log.Debugf("[Config] KentikEmail = %s", cliFlags.kentikEmail)
	log.Debugf("[Config] KentikToken = %s", cliFlags.kentikToken)
	log.Debugf("[Config] Test Interval = %ssec", cliFlags.testInterval)
	log.Debugf("[Config] Test Length = %ssec", cliFlags.testLength)
	log.Debugf("[Config] TSDB download prefix = %s", cliFlags.downloadPrefix)
	log.Debugf("[Config] TSDB upload prefix = %s", cliFlags.uploadPrefix)
	printPerfServers(config.PerfServers)

	if cliFlags.netperf {
		netperfRun(config)
	} else {
		iperfRun(config)
	}
}

func iperfRun(config configuration) {
	if cliFlags.noContainer {
		iperfBinary = "iperf3"
	} else {
		runtime := checkContainerRuntime()
		iperfBinary = fmt.Sprintf("%s run -i --rm %s", runtime, cliFlags.imageRepo)
	}
	log.Debugf("[Config] Perf Binary = %s", cliFlags.perfServerPort)

	// assign the perf server port from config first, then cli, lastly defaults
	if config.ServerPort != "" {
		cliFlags.perfServerPort = config.ServerPort
	}
	log.Debugf("[Config] Perf Server Port = %s", cliFlags.perfServerPort)

	// begin the program loop
	for {
		for _, v := range config.PerfServers {
			for endpointAddress, endpointName := range v {
				if endpointName == "" {
					endpointName = endpointAddress
				}
				// Test the download speed to the iperf endpoint.
				iperfDownResults, err := runCmd(fmt.Sprintf("%s -P %s -t %s -f k -p %s -c %s | tail -n 3 | head -n1 | awk '{print $7}'",
					iperfBinary,
					cliFlags.parallelConn,
					cliFlags.testLength,
					cliFlags.perfServerPort,
					endpointAddress,
				))

				if strings.Contains(iperfDownResults, "error") {
					log.Errorf("Error testing to the target server at %s:%s", endpointAddress, cliFlags.perfServerPort)
					log.Errorf("Verify iperf is running and reachable at %s:%s", endpointAddress, cliFlags.perfServerPort)
					log.Errorln(err, iperfDownResults)
				} else {
					// verify the results are a valid integer and convert to bps for plotting.
					iperfDownResultsBbps, err := convertKbitsToBits(iperfDownResults)
					if err != nil {
						log.Errorf("no valid integer returned from the iperf test, please run with --debug for details")
					}

					// Write the download results to the tsdb.
					log.Infof("Download results for endpoint %s [%s] -> %d bps", endpointAddress, endpointName, iperfDownResultsBbps)
					timeDownNow := time.Now().Unix()
					if cliFlags.tsdbType != "influx" {
						msg := fmt.Sprintf("%s.%s %d %d\n", cliFlags.downloadPrefix, endpointName, iperfDownResultsBbps, timeDownNow)
						sendGraphite("tcp", config.GraphiteHostPort, msg)
					} else {
						msg := fmt.Sprintf("%s,testType=%s,iperfDestination=%s,iperfSource=%s iperfResultsBps=%d",
							config.MeasurementName,
							cliFlags.downloadPrefix,
							endpointName,
							config.Hostname,
							iperfDownResultsBbps,
						)
						log.Errorf("url: %s : payload: %s", config.InfluxURL, msg)
						sendInflux(config.InfluxURL, msg)
					}
				}

				// Test the upload speed to the iperf endpoint.
				iperfUpResults, err := runCmd(fmt.Sprintf("%s -P %s -R -t %s -f k -p %s -c %s | tail -n 3 | head -n1 | awk '{print $7}'",
					iperfBinary,
					cliFlags.parallelConn,
					cliFlags.testLength,
					cliFlags.perfServerPort,
					endpointAddress,
				))

				if strings.Contains(iperfUpResults, "error") {
					log.Errorf("Error testing to the target server at %s:%s", endpointAddress, cliFlags.perfServerPort)
					log.Errorf("Verify iperf is running and reachable at %s:%s", endpointAddress, cliFlags.perfServerPort)
					log.Errorln(err, iperfUpResults)
				} else {
					// verify the results are a valid integer and convert to bps for plotting.
					iperfUpResultsBbps, err := convertKbitsToBits(iperfUpResults)
					if err != nil {
						log.Errorf("no valid integer returned from the iperf test, please run with --debug for details")
					}

					// Write the upload results to the tsdb.
					log.Infof("Upload results for endpoint %s [%s] -> %d bps", endpointAddress, endpointName, iperfUpResultsBbps)
					timeUpNow := time.Now().Unix()
					if cliFlags.tsdbType != "influx" {
						msg := fmt.Sprintf("%s.%s %d %d\n", cliFlags.uploadPrefix, endpointName, iperfUpResultsBbps, timeUpNow)
						sendGraphite("tcp", config.GraphiteHostPort, msg)
					} else {
						msg := fmt.Sprintf("%s,testType=%s,iperfDestination=%s,iperfSource=%s iperfResultsBps=%d", config.MeasurementName,
							cliFlags.uploadPrefix,
							endpointName,
							config.Hostname,
							iperfUpResultsBbps,
						)
						log.Errorf("url: %s : payload: %s", config.InfluxURL, msg)
						sendInflux(config.InfluxURL, msg)
					}
				}
			}
		}
		// polling interval as defined in the configuration file or cli args
		t, _ := time.ParseDuration(string(cliFlags.testInterval) + "s")
		time.Sleep(t)
	}
}

func netperfRun(config configuration) {

	if cliFlags.noContainer {
		netperfBinary = "netperf"
	} else {
		if cliFlags.imageRepo == defaultIperfRepo {

			cliFlags.imageRepo = defaultNetperfRepo
			log.Debugf("[Config] Perf Binary = %s", cliFlags.imageRepo)

		}
		runtime := checkContainerRuntime()
		netperfBinary = fmt.Sprintf("%s run -i --rm %s", runtime, cliFlags.imageRepo)
	}
	log.Debugf("[Config] Perf Binary = %s", netperfBinary)

	// assign the perf server port from config first, then cli, lastly defaults
	if config.ServerPort != "" {
		cliFlags.perfServerPort = config.ServerPort
	}
	if cliFlags.perfServerPort == defaultIperfPort {
		cliFlags.perfServerPort = defaultNetperfPort
	}
	log.Debugf("[Config] Perf Server Port = %s", cliFlags.perfServerPort)

	// begin the program loop
	for {
		for _, v := range config.PerfServers {
			for endpointAddress, endpointName := range v {
				if endpointName == "" {
					endpointName = endpointAddress
				}
				// test the speed to the netserver endpoint, ignoring the err as netserver STDERR is not great.
				iperfDownResults, _ := runCmd(fmt.Sprintf("%s -P 0 -t %s -f k -l %s -p %s -H %s | awk '{print $5}'",
					netperfBinary,
					netperfTCP,
					cliFlags.testLength,
					cliFlags.perfServerPort,
					endpointAddress,
				))
				// the error reporting is not great for netperf so we are basically looking for a word in the STDERR
				if strings.Contains(iperfDownResults, "sure") {
					log.Errorf("Error testing to the target server at %s:%s", endpointAddress, cliFlags.perfServerPort)
					log.Errorf("Verify netserver is running and reachable at %s:%s", endpointAddress, cliFlags.perfServerPort)
				} else {
					// verify the results are a valid integer and convert to bps for plotting.
					iperfDownResultsBbps, err := convertKbitsToBits(iperfDownResults)
					if err != nil {
						log.Errorf("no valid integer returned from the netperf test, please run with --debug for details: %v", err)
					}
					// Write the download results to the tsdb.
					log.Infof("Download results for endpoint %s [%s] -> %d bps", endpointAddress, endpointName, iperfDownResultsBbps)
					timeDownNow := time.Now().Unix()
					if cliFlags.tsdbType != "influx" {
						msg := fmt.Sprintf("%s.%s %d %d\n", cliFlags.downloadPrefix, endpointName, iperfDownResultsBbps, timeDownNow)
						sendGraphite("tcp", config.GraphiteHostPort, msg)
					} else {
						msg := fmt.Sprintf("%s,testType=%s,iperfDestination=%s,iperfSource=%s iperfDownloadResultsBps=%d",
							config.MeasurementName,
							cliFlags.downloadPrefix,
							endpointName,
							config.Hostname,
							iperfDownResultsBbps,
						)
						log.Errorf("url: %s : payload: %s", config.InfluxURL, msg)
						sendInflux(config.InfluxURL, msg)
					}
				}
			}
		}

		// polling interval as defined in the configuration file or cli args
		t, _ := time.ParseDuration(string(cliFlags.testInterval) + "s")
		time.Sleep(t)
	}
}

// runCmd Run the iperf container and return the output and any errors.
func runCmd(command string) (string, error) {
	command = strings.TrimSpace(command)
	var cmd string
	var args []string
	cmd = "/bin/bash"
	args = []string{"-c", command}

	// log the shell command being run if the debug flag is set.
	log.Debugf("[CMD] Running Command -> %s", args)

	output, err := exec.Command(cmd, args...).CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

// sendGraphite write the results to a graphite socket.
func sendGraphite(connType string, socket string, msg string) {
	if cliFlags.debug {
		log.Infof("Sending the following msg to the tsdb: %s", msg)
	}
	conn, err := net.Dial(connType, socket)
	if err != nil {
		log.Errorf("Could not connect to the graphite server -> [%s]", socket)
		log.Errorf("Verify the graphite server is running and reachable at %s", socket)
	} else {
		defer conn.Close()
		_, err = fmt.Fprintf(conn, msg)
		if err != nil {
			log.Errorf("Error writing to the graphite server at -> [%s]", socket)
		}
	}
}

// sendInflux write results to an HTTP endpoint in Influx Line Format
func sendInflux(influxURL string, msg string) (err error) {
	req, err := http.NewRequest("POST", influxURL, bytes.NewBufferString(msg))
	if err != nil {
		log.Errorf("Error constructing URI : %s %s", influxURL, msg)
		return err
	}
	req.Header.Add("Content-Type", "application/influx")
	req.Header.Add("X-CH-Auth-Email", cliFlags.kentikEmail)
	req.Header.Add("X-CH-Auth-API-Token", cliFlags.kentikToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Errorf("Could not connect to the Influx endpoint -> [%s]", influxURL)
		log.Errorf("Verify the Influx server is running and reachable at %s", influxURL)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	//_, err = fmt.Fprint(resp, msg)
	log.Infof("StatusCode: %d", resp.StatusCode)
	log.Infof("Status: %s", resp.Status)
	log.Infof("Body: %s", resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	log.Debug(string([]byte(body)))
	return
}

// checkContainerRuntime checks for docker or podman.
func checkContainerRuntime() string {
	cmd := exec.Command("docker", "--version")
	_, err := cmd.Output()
	if err == nil {
		return "docker"
	}
	cmd = exec.Command("podman", "--version")
	_, err = cmd.Output()
	if err == nil {
		return "podman"
	}
	if err != nil {
		log.Fatal(errors.New("docker or podman is required for container mode, use the flag \"--nocontainer\" to not use containers"))
	}

	return ""
}

// mapPerfDest creates a k/v pair of node address and node name.
func mapPerfDest(tunnelDestPair string) map[string]string {
	tunnelDestMap := make(map[string]string)
	hostAddressPair := splitPerfPair(tunnelDestPair)

	if len(hostAddressPair) > 1 {
		tunnelDestMap[hostAddressPair[0]] = hostAddressPair[1]
		return tunnelDestMap
	}

	if len(hostAddressPair) > 0 {
		tunnelDestMap[hostAddressPair[0]] = ""
		return tunnelDestMap
	}

	return tunnelDestMap
}
