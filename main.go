package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/google/gousb"
	"github.com/google/gousb/usbid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	flag "github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type labels map[string]string

const (
	logLevelAll   = "all"
	logLevelDebug = "debug"
	logLevelInfo  = "info"
	logLevelWarn  = "warn"
	logLevelError = "error"
	logLevelNone  = "none"
)

var (
	usbDebug           = flag.Int("usb-debug", 0, "libusb debug level (0..3)")
	humanReadable      = flag.Bool("human-readable", true, "use human readable label names instead of hex codes, possibly not all codes can be translated")
	kubeconfig         = flag.String("kubeconfig", "", "path to kubeconfig")
	hostname           = flag.String("hostname", "", "Hostname of the node on which this process is running")
	noContain          = flag.StringSlice("no-contain", []string{}, "list of strings, usb devices containing these case-insensitive strings will not be considered for labeling")
	only               = flag.StringSlice("only", []string{}, "list of strings in the format of <vendor id>_<product id>. These usb devices are considered for labeling only. If a provided device is not found, the label value will be set to false.")
	logLevel           = flag.String("log-level", logLevelInfo, fmt.Sprintf("Log level to use. Possible values: %s", availableLogLevels))
	updateTime         = flag.Duration("update-time", 10*time.Second, "renewal time for labels in seconds")
	labelPrefix        = flag.String("label-prefix", "nudl.squat.ai", "prefix for labels")
	addr               = flag.String("listen-address", ":8080", "listen address for prometheus metrics server")
	availableLogLevels = strings.Join([]string{
		logLevelAll,
		logLevelDebug,
		logLevelInfo,
		logLevelWarn,
		logLevelError,
		logLevelNone,
	}, ", ")
)

var (
	reconcilingCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "reconciling_counter",
			Help: "Number of reconciling outcomes",
		},
		[]string{"success"},
	)
	labelGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "number_labels",
			Help: "number of labels that are being managed",
		},
	)
)

// Use global regexps to avoid compiling them multible times.
var (
	regParse *regexp.Regexp = regexp.MustCompile(`^\s*(\S|\S.*\S)\s*\(\s*(\S|\S.*\S)\s*\)$`)
	regTrim  *regexp.Regexp = regexp.MustCompile(`[^\w._-]`)
)

func sPrintLabelKey(k string) string {
	return fmt.Sprintf("%s/%s", *labelPrefix, k)
}

func hexKey(desc *gousb.DeviceDesc) string {
	return fmt.Sprintf("%s_%s", desc.Vendor.String(), desc.Product.String())
}

func humanReadableKey(desc *gousb.DeviceDesc, logger log.Logger) (string, error) {
	vendor := usbid.Vendors[desc.Vendor]
	vendorName := vendor.Name
	var deviceName string
	if device, ok := vendor.Product[desc.Product]; ok {
		deviceName = device.String()
	} else {
		level.Warn(logger).Log("msg", "could not find device name", "vendor", vendorName, "vendorID", desc.Vendor, "product", desc.Product)
		return "", fmt.Errorf("could not find device name")
	}

	// Replace charackters not allowed in node labels.
	vendorName = string(regTrim.ReplaceAll([]byte(vendorName), []byte("-")))
	deviceName = string(regTrim.ReplaceAll([]byte(deviceName), []byte("-")))
	return fmt.Sprintf("%s_%s", vendorName, deviceName), nil
}

// genKey generates a key with prefix labelPrefix out of a device description.
func genKey(desc *gousb.DeviceDesc, logger log.Logger) string {
	var key string
	if *humanReadable {
		var err error
		key, err = humanReadableKey(desc, logger)
		if err != nil {
			level.Error(logger).Log("msg", "could not generate human readable key, falling back to hex encoded usb IDs", "err", err.Error())
			key = hexKey(desc)
		}
		labelKey := sPrintLabelKey(key)
		if len(labelKey) > 63 {
			level.Warn(logger).Log("msg", "label key too long, falling back to hex device name", "humanReadableKey", key, "hexKey", hexKey(desc))
			return sPrintLabelKey(hexKey(desc))
		}
		return labelKey
	}
	return sPrintLabelKey(hexKey(desc))
}

// createLables is a wrapper function to pass it to gousb.Context.OpenDevices().
// The returned function will always return false to not open any usb device.
func createLabels(nl *labels, logger log.Logger) func(*gousb.DeviceDesc) bool {
	return func(desc *gousb.DeviceDesc) bool {
		// Filter the values that are not supposed to be used as labels.
		for _, str := range *noContain {
			if strings.Contains(strings.ToLower(usbid.Describe(desc)), strings.ToLower(str)) {
				return false
			}
		}
		(*nl)[genKey(desc, logger)] = "true"

		return false
	}
}

// scanUSB will return the labels from the scanned usb devices.
func scanUSB(logger log.Logger) (labels, error) {
	ctx := gousb.NewContext()
	defer ctx.Close()

	ctx.Debug(*usbDebug)

	l := make(labels)
	if _, err := ctx.OpenDevices(createLabels(&l, logger)); err != nil {
		return nil, err
	}

	if len(*only) > 0 {
		onlyLabels := make(labels)
		for _, str := range *only {
			_, ok := l[sPrintLabelKey(str)]
			onlyLabels[sPrintLabelKey(str)] = fmt.Sprintf("%t", ok)
		}
		return onlyLabels, nil
	}
	return l, nil
}

// filter will filter a map of strings by its prefix
// and return the filtered labels.
func filter(m map[string]string) labels {
	ret := make(labels)
	for k, v := range m {
		if strings.HasPrefix(k, *labelPrefix) {
			ret[k] = v
		}
	}
	return ret
}

// merge merges labels into a map of strings
// and returns a map, after deleting the keys
// that start with the prefix labelPrefix.
func merge(l map[string]string, ul labels) map[string]string {
	// Delete old labels.
	for k := range filter(l) {
		if _, e := ul[k]; !e {
			delete(l, k)
		}
	}
	// Add new labels to map.
	for k, v := range ul {
		l[k] = v
	}
	return l
}

// getNode returns the node with name hostname or an error.
func getNode(ctx context.Context, clientset *kubernetes.Clientset) (*v1.Node, error) {
	node, err := clientset.CoreV1().Nodes().Get(ctx, *hostname, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return nil, fmt.Errorf("node not found: %w", err)
	} else if err != nil {
		return nil, fmt.Errorf("could not get node: %w", err)
	}
	return node, nil
}

// scanAndLabel scans and labels the node with name hostname or returns an error.
func scanAndLabel(ctx context.Context, clientset *kubernetes.Clientset, logger log.Logger) error {
	node, err := getNode(ctx, clientset)
	if err != nil {
		return err
	}
	oldData, err := json.Marshal(node)
	if err != nil {
		return err
	}
	// Scan usb device.
	nl, err := scanUSB(logger)
	if err != nil {
		return fmt.Errorf("could not scan usb devices: %w", err)
	} else {
		level.Debug(logger).Log("msg", "successfully scanned usb device")
	}
	labelGauge.Set(float64(len(nl)))
	node.ObjectMeta.Labels = merge(node.ObjectMeta.Labels, nl)
	newData, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("failed to marshal labels: %w", err)
	}
	patch, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, v1.Node{})
	if err != nil {
		return fmt.Errorf("failed to create patch for node %q: %w", node.Name, err)
	}
	if nn, err := clientset.CoreV1().Nodes().Patch(ctx, node.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("failed to patch node: %w", err)
	} else {
		level.Debug(logger).Log("msg", fmt.Sprintf("patched labels: %v", nn.ObjectMeta.Labels))
	}
	return nil
}

// cleanUp will remove all labels with the prefix labelPrefix from the node with name hostname or return an error.
func cleanUp(clientset *kubernetes.Clientset, logger log.Logger) error {
	ctx := context.Background()
	node, err := getNode(ctx, clientset)
	if err != nil {
		return err
	}
	oldData, err := json.Marshal(node)
	if err != nil {
		return err
	}
	for k := range node.ObjectMeta.Labels {
		if strings.HasPrefix(k, *labelPrefix) {
			delete(node.ObjectMeta.Labels, k)
		}
	}
	newData, err := json.Marshal(node)
	if err != nil {
		return err
	}

	patch, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, v1.Node{})
	if err != nil {
		return fmt.Errorf("failed to create patch: %w", err)
	}
	if nn, err := clientset.CoreV1().Nodes().Patch(ctx, node.Name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("could not patch node: %w", err)
	} else {
		level.Info(logger).Log("msg", "successfully cleaned node")
		level.Debug(logger).Log("msg", fmt.Sprintf("labels of cleaned node: %v", nn.ObjectMeta.Labels))
	}
	return nil
}

func Main() error {
	flag.Parse()

	logger := log.NewJSONLogger(log.NewSyncWriter(os.Stdout))
	switch *logLevel {
	case logLevelAll:
		logger = level.NewFilter(logger, level.AllowAll())
	case logLevelDebug:
		logger = level.NewFilter(logger, level.AllowDebug())
	case logLevelInfo:
		logger = level.NewFilter(logger, level.AllowInfo())
	case logLevelWarn:
		logger = level.NewFilter(logger, level.AllowWarn())
	case logLevelError:
		logger = level.NewFilter(logger, level.AllowError())
	case logLevelNone:
		logger = level.NewFilter(logger, level.AllowNone())
	default:
		return fmt.Errorf("log level %v unknown; possible values are: %s", *logLevel, availableLogLevels)
	}
	logger = log.With(logger, "ts", log.DefaultTimestampUTC)
	logger = log.With(logger, "caller", log.DefaultCaller)

	if len(*only) > 0 && *humanReadable {
		return fmt.Errorf("only and human-readable flags are mutually exclusive")
	}

	// Create context to be able to cancel calls to the Kubernetes API in clean up.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create prometheus registry instead of using default one.
	r := prometheus.NewRegistry()
	r.MustRegister(
		reconcilingCounter,
		labelGauge,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := http.NewServeMux()
	m.Handle("/metrics", promhttp.HandlerFor(r, promhttp.HandlerOpts{}))
	// Create a global variable for the metrics server to be able to stop it later.
	msrv := &http.Server{
		Addr:    *addr,
		Handler: m,
	}
	go func() {
		level.Info(logger).Log("msg", "starting metrics server")
		if err := msrv.ListenAndServe(); err != nil {
			level.Error(logger).Log("msg", "could not start metrics server", "err", err)
		}
	}()

	// Generate a kubeconfig.
	var config *rest.Config
	var err error
	if *kubeconfig == "" {
		config, err = rest.InClusterConfig()
		if err == rest.ErrNotInCluster {
			return fmt.Errorf("not in cluster: %w", err)
		} else if err != nil {
			return err
		}
		level.Info(logger).Log("msg", "generated in cluster config")
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			return fmt.Errorf("could not generate kubernetes config: %w", err)
		}
		level.Info(logger).Log("msg", fmt.Sprintf("generated config with kubeconfig: %s", *kubeconfig))
	}
	// Create the clientset.
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	level.Info(logger).Log("msg", "start service", "no-contain", *noContain, "label-prefix", *labelPrefix)
	// Use a mutex to avoid simultaneous updates at small update-time or slow network speed.
	var mutex sync.Mutex
	for {
		select {
		case s := <-ch:
			level.Info(logger).Log("msg", fmt.Sprintf("received signal %v", s))
			// Cancel the context for running scan and label routine.
			cancel()
			// Lock mutex to wait until the running scan and label routin is finished.
			mutex.Lock()
			if err := cleanUp(clientset, logger); err != nil {
				level.Error(logger).Log("msg", "could not clean node", "err", err)
			}
			if err := msrv.Close(); err != nil {
				level.Error(logger).Log("msg", "could not close metrics server", "err", err)
			} else {
				level.Info(logger).Log("msg", "closing metrics server")
			}
			level.Info(logger).Log("msg", "shutting down")
			os.Exit(130)
		case <-time.After(*updateTime):
			mutex.Lock()
			// Use a go routine, so the time to update the labels doesn't influence the frequency of updates.
			go func() {
				defer mutex.Unlock()
				if err := scanAndLabel(ctx, clientset, logger); err != nil {
					level.Error(logger).Log("msg", "failed to scan and label", "err", err)
					reconcilingCounter.With(prometheus.Labels{"success": "false"}).Inc()
				} else {
					reconcilingCounter.With(prometheus.Labels{"success": "true"}).Inc()
				}
			}()
		}
	}
}

func main() {
	if err := Main(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}
