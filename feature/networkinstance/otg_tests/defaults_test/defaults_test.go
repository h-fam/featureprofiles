/*
 Copyright 2022 Google LLC

 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at

      https://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
*/

package interface_assignments_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/openconfig/featureprofiles/internal/attrs"
	"github.com/openconfig/featureprofiles/internal/deviations"
	"github.com/openconfig/featureprofiles/internal/fptest"
	"github.com/openconfig/featureprofiles/internal/otgutils"
	"github.com/openconfig/ondatra"
	"github.com/openconfig/ondatra/gnmi"
	"github.com/openconfig/ondatra/gnmi/oc"
	"github.com/openconfig/ygnmi/ygnmi"
	"github.com/openconfig/ygot/ygot"
)

func TestMain(m *testing.M) {
	fptest.RunTests(m)
}

func assignPort(t *testing.T, d *oc.Root, intf, niName string, a *attrs.Attributes) {
	t.Helper()
	d.GetOrCreateNetworkInstance(niName)

	ocInt := a.ConfigOCInterface(&oc.Interface{})
	ocInt.Name = ygot.String(intf)

	if err := d.AppendInterface(ocInt); err != nil {
		t.Fatalf("AddInterface(%v): cannot configure interface %s, %v", ocInt, intf, err)
	}
}

var (
	dutPort1 = &attrs.Attributes{
		IPv4:    "192.0.2.0",
		IPv4Len: 31,
		IPv6:    "2001:db8::1",
		IPv6Len: 64,
	}
	dutPort2 = &attrs.Attributes{
		IPv4:    "192.0.2.2",
		IPv4Len: 31,
		IPv6:    "2001:db8:1::1",
		IPv6Len: 64,
	}
	atePort1 = &attrs.Attributes{
		Name:    "port1",
		IPv4:    "192.0.2.1",
		IPv4Len: 31,
		IPv6:    "2001:db8::2",
		IPv6Len: 64,
		MAC:     "02:00:01:01:01:01",
	}
	atePort2 = &attrs.Attributes{
		Name:    "port2",
		IPv4:    "192.0.2.3",
		IPv4Len: 31,
		IPv6:    "2001:db8:1::2",
		IPv6Len: 64,
		MAC:     "02:00:02:01:01:01",
	}
)

// TestDefaultAddressFamilies verifies that both IPv4 and IPv6 are enabled by default without a need for additional
// configuration within a network instance. It does so by validating that simple IPv4 and IPv6 flows do not experience
// loss.
func TestDefaultAddressFamilies(t *testing.T) {
	dut := ondatra.DUT(t, "dut")

	d := &oc.Root{}
	d.GetOrCreateNetworkInstance(*deviations.DefaultNetworkInstance).Type = oc.NetworkInstanceTypes_NETWORK_INSTANCE_TYPE_DEFAULT_INSTANCE

	// Assign two ports into the default network instance.
	assignPort(t, d, dut.Port(t, "port1").Name(), *deviations.DefaultNetworkInstance, dutPort1)
	assignPort(t, d, dut.Port(t, "port2").Name(), *deviations.DefaultNetworkInstance, dutPort2)

	if *deviations.ExplicitInterfaceInDefaultVRF {
		fptest.AssignToNetworkInstance(t, dut, dut.Port(t, "port1").Name(), *deviations.DefaultNetworkInstance, 0)
		fptest.AssignToNetworkInstance(t, dut, dut.Port(t, "port2").Name(), *deviations.DefaultNetworkInstance, 0)
	}

	fptest.LogQuery(t, "test configuration", gnmi.OC().Config(), d)
	gnmi.Update(t, dut, gnmi.OC().Config(), d)

	ate := ondatra.ATE(t, "ate")
	top := ate.OTG().NewConfig(t)

	p1 := ate.Port(t, "port1")
	p2 := ate.Port(t, "port2")

	atePort1.AddToOTG(top, p1, dutPort1)
	atePort2.AddToOTG(top, p2, dutPort2)
	// Create an IPv4 flow between ATE port 1 and ATE port 2.
	v4Flow := top.Flows().Add().SetName("ipv4")
	v4Flow.Metrics().SetEnable(true)
	e1 := v4Flow.Packet().Add().Ethernet()
	e1.Src().SetValue(atePort1.MAC)
	v4Flow.TxRx().Device().SetTxNames([]string{fmt.Sprintf("%s.IPv4", atePort1.Name)}).SetRxNames([]string{fmt.Sprintf("%s.IPv4", atePort2.Name)})
	v4 := v4Flow.Packet().Add().Ipv4()
	v4.Src().SetValue(atePort1.IPv4)
	v4.Dst().SetValue(atePort2.IPv4)

	// Create an IPv6 flow between ATE port 1 and ATE port 2.
	v6Flow := top.Flows().Add().SetName("ipv6")
	v6Flow.Metrics().SetEnable(true)
	e2 := v6Flow.Packet().Add().Ethernet()
	e2.Src().SetValue(atePort1.MAC)
	v6Flow.TxRx().Device().SetTxNames([]string{fmt.Sprintf("%s.IPv6", atePort1.Name)}).SetRxNames([]string{fmt.Sprintf("%s.IPv6", atePort2.Name)})
	v6 := v6Flow.Packet().Add().Ipv6()
	v6.Src().SetValue(atePort1.IPv6)
	v6.Dst().SetValue(atePort2.IPv6)

	ate.OTG().PushConfig(t, top)
	ate.OTG().StartProtocols(t)

	// TODO(robjs): check with Octavian why this is required.
	time.Sleep(10 * time.Second)
	for _, i := range []string{atePort1.Name, atePort2.Name} {
		t.Logf("checking for ARP on %s", i)
		gnmi.WatchAll(t, ate.OTG(), gnmi.OTG().Interface(i+".Eth").Ipv4NeighborAny().LinkLayerAddress().State(), time.Minute, func(val *ygnmi.Value[string]) bool {
			return val.IsPresent()
		}).Await(t)
	}

	ate.OTG().StartTraffic(t)
	time.Sleep(15 * time.Second)
	ate.OTG().StopTraffic(t)

	otgutils.LogFlowMetrics(t, ate.OTG(), top)
	otgutils.LogPortMetrics(t, ate.OTG(), top)

	// Check that we did not lose any packets for the IPv4 and IPv6 flows.
	for _, flow := range []string{"ipv4", "ipv6"} {
		m := gnmi.Get(t, ate.OTG(), gnmi.OTG().Flow(flow).State())
		tx := m.GetCounters().GetOutPkts()
		rx := m.GetCounters().GetInPkts()
		loss := tx - rx
		lossPct := loss * 100 / tx
		if got := lossPct; got > 0 {
			t.Errorf("LossPct for flow %s: got %v, want 0", flow, got)
		}
	}
}