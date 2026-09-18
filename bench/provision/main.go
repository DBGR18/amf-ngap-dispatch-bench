// Command provision bulk-inserts (or deletes) free5gc subscribers directly in
// MongoDB, so a registration-storm benchmark can provision a fixed batch of
// UEs repeatably without hand-editing the webconsole.
//
// It reuses the verified data constructors and Mongo insert/delete helpers
// from free5gc's own test module (test/mongodb.go, test/ranUe.go) instead of
// re-deriving the per-collection document shapes.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/free5gc/util/mongoapi"

	f5test "test"
)

// Credentials must match the free-ran-ue simulator defaults in
// free-ran-ue/config/ue.yaml (encPermanentKey / encOpcKey / sequenceNumber).
const (
	defaultK   = "8baf473f2f8fd09487cccbd7097c6862"
	defaultOPc = "8e27b6af0e692e750f32667a3b14605d"
	defaultSQN = "000000000023"
)

// webAuthenticationSubscription has no matching Del* helper in
// test/mongodb.go (DelUeFromMongoDB does not clean it up either), so its
// collection name is reproduced here to delete it directly via mongoapi.
const webAuthCollection = "subscriptionData.authenticationData.webAuthenticationSubscription"

func main() {
	n := flag.Int("n", 400, "number of subscribers to provision")
	start := flag.Int("start", 1, "starting MSIN as an integer; IMSIs are contiguous from here")
	mcc := flag.String("mcc", "208", "Mobile Country Code")
	mnc := flag.String("mnc", "93", "Mobile Network Code")
	del := flag.Bool("delete", false, "delete the range instead of inserting it")
	dbURL := flag.String("dburl", "mongodb://localhost:27017", "MongoDB connection URL")
	dbName := flag.String("dbname", "free5gc", "MongoDB database name")
	flag.Parse()

	if *n <= 0 {
		log.Fatalf("-n must be positive, got %d", *n)
	}
	if *start < 0 {
		log.Fatalf("-start must be non-negative, got %d", *start)
	}

	if err := mongoapi.SetMongoDB(*dbName, *dbURL); err != nil {
		log.Fatalf("failed to connect to MongoDB %s (db=%s): %+v", *dbURL, *dbName, err)
	}

	servingPlmnId := *mcc + *mnc

	mode := "insert"
	if *del {
		mode = "delete"
	}
	firstSupi := makeSupi(*mcc, *mnc, *start)
	lastSupi := makeSupi(*mcc, *mnc, *start+*n-1)
	fmt.Printf("provision: mode=%s count=%d servingPlmnId=%s db=%s@%s range=%s..%s\n",
		mode, *n, servingPlmnId, *dbName, *dbURL, firstSupi, lastSupi)

	for i := 0; i < *n; i++ {
		msin := *start + i
		supi := makeSupi(*mcc, *mnc, msin)

		if *del {
			deleteUe(supi, servingPlmnId)
		} else {
			insertUe(supi, servingPlmnId)
		}

		if (i+1)%50 == 0 {
			fmt.Printf("progress: %d/%d %sd (last supi=%s)\n", i+1, *n, mode, supi)
		}
	}

	fmt.Printf("summary: %sd %d subscriber(s), supi range %s..%s, servingPlmnId=%s\n",
		mode, *n, firstSupi, lastSupi, servingPlmnId)
	os.Exit(0)
}

func makeSupi(mcc, mnc string, msin int) string {
	return fmt.Sprintf("imsi-%s%s%010d", mcc, mnc, msin)
}

// insertUe follows the exact call sequence and argument order shown by
// test/mongodb.go:467 InsertUeToMongoDB, calling the individual Insert*
// helpers directly instead (InsertUeToMongoDB itself requires a *testing.T).
func insertUe(supi, servingPlmnId string) {
	authSubs := f5test.GetAuthSubscription(defaultK, defaultOPc, "")
	// GetAuthSubscription (test/ranUe.go:58) already sets
	// AuthenticationManagementField to "8000", matching the simulator
	// default, but it hardcodes SequenceNumber.Sqn to the TS 35.208 test
	// set 19 value ("16f3b3f70fc2"), which does NOT match the simulator's
	// config default of "000000000023" (free-ran-ue/config/ue.yaml) -- so
	// it must be overridden here.
	authSubs.SequenceNumber.Sqn = defaultSQN

	f5test.InsertAuthSubscriptionToMongoDB(supi, authSubs)
	f5test.InsertWebAuthSubscriptionToMongoDB(supi, authSubs)

	amData := f5test.GetAccessAndMobilitySubscriptionData()
	f5test.InsertAccessAndMobilitySubscriptionDataToMongoDB(supi, amData, servingPlmnId)

	smfSelData := f5test.GetSmfSelectionSubscriptionData()
	f5test.InsertSmfSelectionSubscriptionDataToMongoDB(supi, smfSelData, servingPlmnId)

	// InsertSessionManagementSubscriptionDataToMongoDB (mongodb.go:137) uses
	// mongoapi.RestfulAPIPostMany, which is a plain InsertMany with no
	// filter/upsert check (mongoapi.go:435) -- unlike the PutOne-backed
	// helpers above, calling it twice would duplicate documents. Delete any
	// existing docs for this ueId+servingPlmnId first so repeated runs of
	// this tool stay idempotent.
	if err := f5test.DelSessionManagementSubscriptionDataFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("pre-insert cleanup of smData failed for %s: %+v", supi, err)
	}
	smSelData := f5test.GetSessionManagementSubscriptionData()
	f5test.InsertSessionManagementSubscriptionDataToMongoDB(supi, servingPlmnId, smSelData)

	amPolicyData := f5test.GetAmPolicyData()
	f5test.InsertAmPolicyDataToMongoDB(supi, amPolicyData)

	smPolicyData := f5test.GetSmPolicyData()
	f5test.InsertSmPolicyDataToMongoDB(supi, smPolicyData)

	// Same PostMany non-upsert issue as smData above.
	if err := f5test.DelChargingDataFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("pre-insert cleanup of chargingData failed for %s: %+v", supi, err)
	}
	chargingDatas := f5test.GetChargingData()
	f5test.InsertChargingDataToMongoDB(supi, servingPlmnId, chargingDatas)

	if err := f5test.DelFlowRuleFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("pre-insert cleanup of flowRule failed for %s: %+v", supi, err)
	}
	flowRules := f5test.GetFlowRuleData()
	f5test.InsertFlowRuleToMongoDB(supi, servingPlmnId, flowRules)

	if err := f5test.DelQosFlowFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("pre-insert cleanup of qosFlow failed for %s: %+v", supi, err)
	}
	qosFlows := f5test.GetQosFlowData()
	f5test.InsertQoSFlowToMongoDB(supi, servingPlmnId, qosFlows)
}

// deleteUe removes every collection insertUe wrote to, using the Del*
// helpers from test/mongodb.go plus a direct mongoapi call for
// webAuthenticationSubscription (no Del* helper exists for it upstream).
func deleteUe(supi, servingPlmnId string) {
	if err := f5test.DelAuthSubscriptionToMongoDB(supi); err != nil {
		log.Fatalf("delete authSubscription failed for %s: %+v", supi, err)
	}
	if err := mongoapi.RestfulAPIDeleteMany(webAuthCollection, bson.M{"ueId": supi}); err != nil {
		log.Fatalf("delete webAuthenticationSubscription failed for %s: %+v", supi, err)
	}
	if err := f5test.DelAccessAndMobilitySubscriptionDataFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("delete amData failed for %s: %+v", supi, err)
	}
	if err := f5test.DelSmfSelectionSubscriptionDataFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("delete smfSelectionSubscriptionData failed for %s: %+v", supi, err)
	}
	if err := f5test.DelSessionManagementSubscriptionDataFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("delete smData failed for %s: %+v", supi, err)
	}
	if err := f5test.DelAmPolicyDataFromMongoDB(supi); err != nil {
		log.Fatalf("delete amPolicyData failed for %s: %+v", supi, err)
	}
	if err := f5test.DelSmPolicyDataFromMongoDB(supi); err != nil {
		log.Fatalf("delete smPolicyData failed for %s: %+v", supi, err)
	}
	if err := f5test.DelChargingDataFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("delete chargingData failed for %s: %+v", supi, err)
	}
	if err := f5test.DelFlowRuleFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("delete flowRule failed for %s: %+v", supi, err)
	}
	if err := f5test.DelQosFlowFromMongoDB(supi, servingPlmnId); err != nil {
		log.Fatalf("delete qosFlow failed for %s: %+v", supi, err)
	}
}
