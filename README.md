# nudl

Node Usb Device Labeler - label Kubernetes nodes according to their USB devices and kernel modules.

## Usage

Apply the example configuration
```bash
kubectl apply -f https://raw.githubusercontent.com/leonnicolas/nudl/main/example.yaml
```

### Configure the Labeler
```
Usage of ./nudl:
      --hostname string         Hostname of the node on which this process is running
      --human-readable          use human readable label names instead of hex codes, possibly not all codes can be translated (default true)
      --kubeconfig string       path to kubeconfig
  -m, --label-mod strings       list of strings, kernel modules matching a string will be used as labels with values true, if found
      --label-prefix string     prefix for labels (default "nudl.squat.ai")
      --listen-address string   listen address for prometheus metrics server (default ":8080")
      --log-level string        Log level to use. Possible values: all, debug, info, warn, error, none (default "info")
      --no-contain strings      list of strings, usb devices containing these case-insensitive strings will not be considered for labeling
      --update-time duration    renewal time for labels in seconds (default 10s)
      --usb-debug int           libusb debug level (0..3)
```

__Note:__ to check kernel modules, __nudl__ needs access to the file _/proc/modules_.

### Labels USB devices

If __--human-readable=false__, vendor and device codes will be four hex characters each. The generated label will be of the form:
```
<label_prefix>/<vendor>_<device>=true
```
for example:
```
nudl.squat.ai/04f2_b420=true
```
Otherwise __nudl__ will try to translate the vendor and device codes into human readable strings using the [usbid](https://godoc.org/github.com/google/gousb/usbid) package, which uses [http://www.linux-usb.org/usb.ids](http://www.linux-usb.org/usb.ids). If the codes are not found, the name defaults to _Unknown_. Since some characters are not allowed in Kubernetes labels, forbidden characters are converted into "-".

The above example would look like:
```
nudl.sqiat.ai/Chicony-Electronics-Co.--Ltd_Unknown:true
```

Check out [http://www.linux-usb.org/usb-ids.html](http://www.linux-usb.org/usb-ids.html) for more information about what devices are known.

### Exclude USB devices
Use the `--no-contain` flag to exclude USB devices that can be ignored, e.g. USB hubs.

### Label Kernel Modules
You can label your nodes according to their kernel modules. Use the `--label-mod` flag to pass a list of strings. If a kernel module found in __/proc/modules__ matches one of the input strings, then the node will be given a label of the format:
```
<label prefix>/<module name>=true
```
for example:
```
nudl.squat.ai/wireguard=true
```
If the module is not found, the flag's value will be set to _false_.
 
### Outside the cluster
```bash
docker run --rm -v ~/.kube:/mnt leonnicolas/nudl --kubeconfig /mnt/k3s.yaml --label-mod="wireguard,fantasy" --hostname example_host
```
