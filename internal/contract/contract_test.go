package contract

import (
	"errors"
	"testing"
)

func TestErrorCodeSurvivesForeignException(t *testing.T) {
	err := errors.New(Fail("invalid_config", "broken config").Error())
	if value := ErrorValue(err); value.Code != "invalid_config" || value.Message != "broken config" {
		t.Fatal(value)
	}
}
func TestNetworkRejectsAmbiguousOrInvalidSnapshot(t *testing.T) {
	for _, network := range []Network{
		{DefaultInterface: 0},
		{DefaultInterface: 2, Interfaces: []Interface{{Index: 1, Name: "wlan0", MTU: 1500}}},
		{DefaultInterface: 1, Interfaces: []Interface{{Index: 1, Name: "wlan0", MTU: 1500, Addresses: []string{"bad"}}}},
		{DefaultInterface: 1, Interfaces: []Interface{{Index: 1, Name: "wlan0", MTU: 1500, DNSServers: []string{"example.com"}}}},
		{DefaultInterface: 1, Interfaces: []Interface{{Index: 1, Name: "a", MTU: 1500}, {Index: 1, Name: "b", MTU: 1500}}},
	} {
		if err := network.Validate(); err == nil {
			t.Fatalf("accepted %+v", network)
		}
	}
	if err := (Network{DefaultInterface: -1}).Validate(); err != nil {
		t.Fatal(err)
	}
}
