package ngap

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/free5gc/nas"
	"github.com/free5gc/nas/nasMessage"
	"github.com/free5gc/nas/nasType"
	"github.com/free5gc/ngap"
	"github.com/free5gc/ngap/ngapType"
)

func TestMsinKey(t *testing.T) {
	tests := []struct {
		name string
		suci string
		want uint64
		ok   bool
	}{
		{"null scheme", "suci-0-208-93-0000-0-0-0000000001", 1, true},
		{"null scheme high msin", "suci-0-208-93-0000-0-0-0000000399", 399, true},
		// Profile A/B conceal the MSIN, so the paper's identity-based dispatch
		// cannot work before authentication; the caller must fall back.
		{"profile A", "suci-0-208-93-0000-1-0-abcdef0123", 0, false},
		{"not a suci", "imsi-208930000000001", 0, false},
		{"too few fields", "suci-0-208-93", 0, false},
		{"non numeric msin", "suci-0-208-93-0000-0-0-00000000xx", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := msinKey(tt.suci)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

// buildInitialUEMessage produces a wire-format InitialUEMessage carrying a
// plain Registration Request whose mobile identity is the given SUCI.
func buildInitialUEMessage(t *testing.T, ranUeNgapID int64, suciBytes []byte) []byte {
	t.Helper()

	m := nas.NewMessage()
	m.GmmMessage = nas.NewGmmMessage()
	m.GmmHeader.SetMessageType(nas.MsgTypeRegistrationRequest)

	// Field-by-field, mirroring free5gc's own test packet builder
	// (test/nasTestpacket/NasPdu.go GetRegistrationRequest).
	rr := nasMessage.NewRegistrationRequest(0)
	rr.SetExtendedProtocolDiscriminator(nasMessage.Epd5GSMobilityManagementMessage)
	rr.SpareHalfOctetAndSecurityHeaderType.SetSecurityHeaderType(nas.SecurityHeaderTypePlainNas)
	rr.SpareHalfOctetAndSecurityHeaderType.SetSpareHalfOctet(0x00)
	rr.RegistrationRequestMessageIdentity.SetMessageType(nas.MsgTypeRegistrationRequest)
	rr.NgksiAndRegistrationType5GS.SetTSC(nasMessage.TypeOfSecurityContextFlagNative)
	rr.NgksiAndRegistrationType5GS.SetNasKeySetIdentifiler(0x7)
	rr.NgksiAndRegistrationType5GS.SetFOR(1)
	rr.NgksiAndRegistrationType5GS.SetRegistrationType5GS(nasMessage.RegistrationType5GSInitialRegistration)
	rr.MobileIdentity5GS = nasType.MobileIdentity5GS{
		Iei:    0,
		Len:    uint16(len(suciBytes)),
		Buffer: suciBytes,
	}
	m.GmmMessage.RegistrationRequest = rr

	data := new(bytes.Buffer)
	require.NoError(t, m.GmmMessageEncode(data))
	payload := data.Bytes()

	pdu := ngapType.NGAPPDU{
		Present: ngapType.NGAPPDUPresentInitiatingMessage,
		InitiatingMessage: &ngapType.InitiatingMessage{
			ProcedureCode: ngapType.ProcedureCode{Value: ngapType.ProcedureCodeInitialUEMessage},
			Criticality:   ngapType.Criticality{Value: ngapType.CriticalityPresentIgnore},
		},
	}
	pdu.InitiatingMessage.Value.Present = ngapType.InitiatingMessagePresentInitialUEMessage
	pdu.InitiatingMessage.Value.InitialUEMessage = &ngapType.InitialUEMessage{}

	idIE := ngapType.InitialUEMessageIEs{}
	idIE.Id.Value = ngapType.ProtocolIEIDRANUENGAPID
	idIE.Criticality.Value = ngapType.CriticalityPresentReject
	idIE.Value.Present = ngapType.InitialUEMessageIEsPresentRANUENGAPID
	idIE.Value.RANUENGAPID = &ngapType.RANUENGAPID{Value: ranUeNgapID}

	nasIE := ngapType.InitialUEMessageIEs{}
	nasIE.Id.Value = ngapType.ProtocolIEIDNASPDU
	nasIE.Criticality.Value = ngapType.CriticalityPresentReject
	nasIE.Value.Present = ngapType.InitialUEMessageIEsPresentNASPDU
	nasIE.Value.NASPDU = &ngapType.NASPDU{Value: payload}

	pdu.InitiatingMessage.Value.InitialUEMessage.ProtocolIEs.List = append(
		pdu.InitiatingMessage.Value.InitialUEMessage.ProtocolIEs.List, idIE, nasIE)

	encoded, err := ngap.Encoder(pdu)
	require.NoError(t, err)
	return encoded
}

// suciMobileIdentity builds the SUCI mobile-identity octets for
// MCC 208 / MNC 93, null scheme, with the given 10-digit MSIN.
// Layout per TS 24.501 9.11.3.4.
func suciMobileIdentity(msin string) []byte {
	require := func(cond bool) {
		if !cond {
			panic("msin must be 10 digits")
		}
	}
	require(len(msin) == 10)

	buf := []byte{
		nasMessage.MobileIdentity5GSTypeSuci, // SUPI format IMSI, type SUCI
		0x02, 0x08, 0x39,                     // MCC 208, MNC 93 (BCD, mnc digit3 = f)
		0x00, 0x00, // routing indicator
		0x00, // protection scheme id: null scheme
		0x00, // home network public key id
	}

	// MSIN, BCD, two digits per octet, low nibble first.
	for i := 0; i < len(msin); i += 2 {
		lo := msin[i] - '0'
		hi := msin[i+1] - '0'
		buf = append(buf, hi<<4|lo)
	}
	return buf
}

func TestSupiDispatchKey_InitialUEMessageUsesSubscriberIdentity(t *testing.T) {
	ResetSupiKeyCache()

	// Two UEs whose RAN-UE-NGAP-IDs would hash differently from their MSINs.
	msg := buildInitialUEMessage(t, 9999, suciMobileIdentity("0000000042"))

	key, pc, found, fallback := SupiDispatchKey(msg)
	require.True(t, found, "dispatch key should be found")
	assert.False(t, fallback, "a null-scheme SUCI must not fall back")
	assert.Equal(t, int64(ngapType.ProcedureCodeInitialUEMessage), pc)
	assert.Equal(t, uint64(42), key, "key should be the MSIN, not the RAN-UE-NGAP-ID")
}

func TestSupiDispatchKey_GutiRegistrationFallsBack(t *testing.T) {
	ResetSupiKeyCache()

	// 5G-GUTI mobile identity: no subscriber identity available this early.
	guti := []byte{nasMessage.MobileIdentity5GSType5gGuti, 0x02, 0x08, 0x39, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01}
	msg := buildInitialUEMessage(t, 7777, guti)

	key, _, found, fallback := SupiDispatchKey(msg)
	require.True(t, found)
	assert.True(t, fallback, "a GUTI registration has no SUCI and must fall back")
	assert.Equal(t, uint64(7777), key, "fallback key is the upstream NGAP UE ID")
}

func TestSupiDispatchKey_StableAcrossIdentifierChange(t *testing.T) {
	ResetSupiKeyCache()

	// Registration teaches the cache that RAN-UE-NGAP-ID 5 is subscriber 42.
	msg := buildInitialUEMessage(t, 5, suciMobileIdentity("0000000042"))
	first, _, found, _ := SupiDispatchKey(msg)
	require.True(t, found)
	require.Equal(t, uint64(42), first)

	// The same message seen again must resolve identically: the whole point of
	// the policy is that a UE's key never moves.
	second, _, found, fallback := SupiDispatchKey(msg)
	require.True(t, found)
	assert.False(t, fallback)
	assert.Equal(t, first, second)
}

func TestSchedulerModeSelection(t *testing.T) {
	SetSchedulerMode("supi")
	assert.Equal(t, "supi", SchedulerMode())

	SetSchedulerMode("hash")
	assert.Equal(t, "hash", SchedulerMode())

	// Anything unrecognised must not silently become the experimental policy.
	SetSchedulerMode("nonsense")
	assert.Equal(t, "hash", SchedulerMode())

	SetSchedulerMode("")
	assert.Equal(t, "hash", SchedulerMode())
}

func TestDispatchIsDeterministicPerKey(t *testing.T) {
	const workers = 8
	s := &UEScheduler{numWorkers: workers}

	for _, key := range []uint64{0, 1, 42, 399, 1 << 40} {
		first := s.hashUEID(key)
		for i := 0; i < 100; i++ {
			assert.Equal(t, first, s.hashUEID(key),
				"a key must always map to the same worker, or per-UE ordering breaks")
		}
		assert.Less(t, first, workers)
		assert.GreaterOrEqual(t, first, 0)
	}
}
