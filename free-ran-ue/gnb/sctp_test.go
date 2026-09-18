package gnb

import (
	"testing"

	"github.com/go-playground/assert"
)

var testGetAmfAndGnbSctpN2AddrCases = []struct {
	name      string
	amfN2Ip   string
	gnbN2Ip   string
	amfN2Port int
	gnbN2Port int
}{
	{
		name:      "testGetAmfAndGnbSctpN2Addr",
		amfN2Ip:   "127.0.0.18",
		gnbN2Ip:   "127.0.0.1",
		amfN2Port: 38412,
		gnbN2Port: 38413,
	},
}

func TestGetAmfAndGnbSctpN2Addr(t *testing.T) {
	for _, testCase := range testGetAmfAndGnbSctpN2AddrCases {
		t.Run(testCase.name, func(t *testing.T) {
			amfAddr, gnbAddr, err := getAmfAndGnbSctpN2Addr(testCase.amfN2Ip, testCase.gnbN2Ip, testCase.amfN2Port, testCase.gnbN2Port)
			assert.Equal(t, nil, err)
			assert.Equal(t, testCase.amfN2Ip, amfAddr.IPAddrs[0].String())
			assert.Equal(t, testCase.gnbN2Ip, gnbAddr.IPAddrs[0].String())
			assert.Equal(t, testCase.amfN2Port, amfAddr.Port)
			assert.Equal(t, testCase.gnbN2Port, gnbAddr.Port)
		})
	}
}
