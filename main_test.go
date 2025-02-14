package main

import (
	"os"
	"testing"

	"github.com/go-kit/log"
	"github.com/google/gousb"
)

func TestGenKey(t *testing.T) {
	tests := []struct {
		name          string
		desc          gousb.DeviceDesc
		want          string
		humanReadable bool
	}{
		{
			name:          "short label",
			want:          "nudl.squat.ai/8086_0044",
			humanReadable: false,
			desc: gousb.DeviceDesc{
				Vendor:  0x8086,
				Product: 0x0044,
			},
		},
		{
			name:          "short label human readable",
			want:          "nudl.squat.ai/Intel-Corp._CPU-DRAM-Controller",
			humanReadable: true,
			desc: gousb.DeviceDesc{
				Vendor:  0x8086,
				Product: 0x0044,
			},
		},
		{
			name:          "long label",
			want:          "nudl.squat.ai/8086_0200",
			humanReadable: false,
			desc: gousb.DeviceDesc{
				Vendor:  0x8086,
				Product: 0x0200,
			},
		},
		{
			name:          "long label human readable fallback to hex",
			want:          "nudl.squat.ai/8086_0200",
			humanReadable: true,
			desc: gousb.DeviceDesc{
				Vendor:  0x8086,
				Product: 0x0200,
			},
		},
		{
			name:          "device not found",
			want:          "nudl.squat.ai/0001_0001",
			humanReadable: true,
			desc: gousb.DeviceDesc{
				Vendor:  0x0001,
				Product: 0x0001,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			humanReadable = &tc.humanReadable

			got := genKey(&tc.desc, log.NewLogfmtLogger(os.Stdout))
			if got != tc.want {
				t.Errorf("genKey() = %q; want %q", got, tc.want)
			}
		})
	}
}
