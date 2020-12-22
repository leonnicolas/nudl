# nudl

Node Usb Device Labeler - label nodes in kubernetes, according to their usb devices.

## Usage

Apply the example configuration
```bash
kubectl apply -f https://raw.githubusercontent.com/leonnicolas/nudl/main/example.yaml
```

### Configure the Labeler
```bash
Usage of ./nudl:
      --hostname string         Hostname of the node on which this process is running
      --human-readable          use human readable label names instead of hex codes, possibly not all codes can be translated (default true)
      --kubeconfig string       path to kubeconfig
      --label-prefix string     prefix for labels (default "nudl.squat.ai")
      --listen-address string   listen address for prometheus metrics server (default ":8080")
      --log-level string        Log level to use. Possible values: all, debug, info, warn, error, none (default "info")
      --no-contain strings      list of strings, usb devices containing these case-insensitive strings will not be considered for labeling
      --update-time duration    renewal time for labels in seconds (default 10s)
      --usb-debug int           libusb debug level (0..3)
```

### Labels of USB devices

If __--human-readable=false__, the names of vendor and device will be four hex charackters each. The generated label will be 
```
<label_prefix>/<vendor>_<device>=true
```
for example:
```
nudl.example.com/04f2_b420=true
```
Otherwise __nudle__ will try to translate the vednor and device codes into human readable strings with the [usbid](https://godoc.org/github.com/google/gousb/usbid) package that uses [http://www.linux-usb.org/usb.ids](http://www.linux-usb.org/usb.ids). If the codes are not found the name defaults to _Unknown_. Since some charackters are not allowed in kubernetes labels, forbitten charackters are converted into "-".

The above example would look like this
```
nudl.example.com/Chicony-Electronics-Co.--Ltd_Unknown:true
```

Check out [http://www.linux-usb.org/usb-ids.html](http://www.linux-usb.org/usb-ids.html) for more information about what devices are known.

### Exclude USB devices
If you don't care about usb hubs or other things, use the --no-contain flag.

### Outside the cluster

```bash
docker run --rm -v ~/.kube:/mnt leonnicolas/nudl --kubeconfig /mnt/k3s.yaml --hostname example_host
```
