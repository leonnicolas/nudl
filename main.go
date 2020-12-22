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

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/google/gousb"
	"github.com/google/gousb/usbid"
	"github.com/prometheus/client_golang/prometheus"
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
	scanUSBErr = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "usb_scan_errors_total",
			Help: "total errors in usb scans",
		},
	)
	labelGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "number_labels",
			Help: "number of labels that are being managed",
		},
	)
)

// have global regexps to avoid compiling them multible times
var (
	regParse *regexp.Regexp
	regTrim  *regexp.Regexp
)

func genKey(desc *gousb.DeviceDesc) string {
	var key string
	if *humanReadable {
		// parse vendor and device from usbid
		dev := usbid.Describe(desc)
		device := regParse.ReplaceAll([]byte(dev), []byte("$1"))
		vendor := regParse.ReplaceAll([]byte(dev), []byte("$2"))
		// replace charackters not allowed in node labels
		vendor = regTrim.ReplaceAll([]byte(vendor), []byte("-"))
		device = regTrim.ReplaceAll([]byte(device), []byte("-"))
		key = fmt.Sprintf("%s_%s", vendor, device)
	} else {
		key = fmt.Sprintf("%s_%s", desc.Vendor.String(), desc.Product.String())
	}
	return fmt.Sprintf("%s/%s", *labelPrefix, key)
}

func createLabels(nl *labels) func(*gousb.DeviceDesc) bool {
	return func(desc *gousb.DeviceDesc) bool {
		// filter values that are not supposed to be used as labels
		for _, str := range *noContain {
			if strings.Contains(strings.ToLower(usbid.Describe(desc)), strings.ToLower(str)) {
				return false
			}
		}
		(*nl)[genKey(desc)] = "true"
		return false
	}
}

func scanUSB() (labels, error) {
	ctx := gousb.NewContext()
	defer ctx.Close()

	ctx.Debug(*usbDebug)

	l := make(labels)
	if _, err := ctx.OpenDevices(createLabels(&l)); err != nil {
		return nil, err
	}
	return l, nil
}

func filter(m map[string]string) labels {
	ret := make(labels)
	for k, v := range m {
		if strings.HasPrefix(k, *labelPrefix) {
			ret[k] = v
		}
	}
	return ret
}

func (l1 labels) addLabels(l2 labels) {
	for k, v := range l2 {
		l1[k] = v
	}
}

func merge(l map[string]string, ul labels) map[string]string {
	// delete old labels
	for k := range filter(l) {
		if _, e := ul[k]; !e {
			delete(l, k)
		}
	}
	// add new labels to map
	for k, v := range ul {
		l[k] = v
	}
	return l
}

func getNode(ctx context.Context, clientset *kubernetes.Clientset) (*v1.Node, error) {
	node, err := clientset.CoreV1().Nodes().Get(ctx, *hostname, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return nil, fmt.Errorf("node not found: %w", err)
	} else if err != nil {
		return nil, fmt.Errorf("could not get node: %w", err)
	}
	return node, nil
}

func scanAndLabel(ctx context.Context, clientset *kubernetes.Clientset, logger log.Logger) error {
	node, err := getNode(ctx, clientset)
	if err != nil {
		return err
	}
	oldData, err := json.Marshal(node)
	if err != nil {
		return err
	}
	// scan usb device
	nl, err := scanUSB()
	if err != nil {
		return fmt.Errorf("couldn not scan usb devices: %w", err)
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
	// compile regexps
	regParse = regexp.MustCompile(`^\s*(\S|\S.*\S)\s*\(\s*(\S|\S.*\S)\s*\)$`)
	regTrim = regexp.MustCompile(`[^\w._-]`)

	// create context to be able to cancel calls to the kubernetes API in clean up
	ctx, cancel := context.WithCancel(context.Background())

	// create prometheus registry to not use default one
	r := prometheus.NewRegistry()
	r.MustRegister(
		reconcilingCounter,
		labelGauge,
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	)
	m := http.NewServeMux()
	m.Handle("/metrics", promhttp.HandlerFor(r, promhttp.HandlerOpts{}))
	// create global var for server to be able to stop the server later
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

	// generate kubeconfig
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
	// create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	level.Info(logger).Log("msg", "start service", "no-contain", *noContain, "label-prefix", *labelPrefix)
	// use a mutex to avoid simultaneous updates at small update-time or slow network speed
	var mutex sync.Mutex
	for {
		select {
		case s := <-ch:
			level.Info(logger).Log("msg", fmt.Sprintf("received signal %v", s))
			// cancel context for running scan and label routine
			cancel()
			// lock mutex to wait until running scan and label routin is finished
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
			// use a go routine, so the time to update the labels doesn't influence the frequency of updates
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
