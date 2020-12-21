# nudl

Node Usv Device Labele - label nodes in kubernetes, according to their usb devices and kernel modules.

## Usage

### Outside the cluster

```bash
docker run --rm -v ~/.kube:/mnt -v /proc/modules:/proc/modules  leonnicolas/nudel --kubeconfig /mnt/k3s.yaml --label-mod="wireguard,fantasy"
```
