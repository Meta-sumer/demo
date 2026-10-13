package models

import (
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestAccountTypeDefaults(t *testing.T) {
	if NormAccountType("") != AccountLive || NormAccountType("junk") != AccountLive || NormAccountType("DEMO") != AccountDemo {
		t.Fatal("NormAccountType")
	}
	w := ToWalletView(&Wallet{})
	if w.AccountType != AccountLive {
		t.Fatal("a wallet stored without accountType is LIVE")
	}
	tv := ToTradeView(&BinaryTrade{})
	raw, _ := json.Marshal(tv)
	if !strings.Contains(string(raw), `"accountType":"LIVE"`) {
		t.Fatalf("trade view must carry accountType: %s", raw)
	}
}

func TestLegacyWalletIndexKey(t *testing.T) {
	if !IsLegacyWalletIndexKey(bson.D{{Key: "userId", Value: int32(1)}, {Key: "currency", Value: int32(1)}}) {
		t.Fatal("legacy key not recognised")
	}
	if IsLegacyWalletIndexKey(bson.D{{Key: "userId", Value: int32(1)}, {Key: "accountType", Value: int32(1)}, {Key: "currency", Value: int32(1)}}) {
		t.Fatal("new key must not be dropped")
	}
	if IsLegacyWalletIndexKey(bson.D{{Key: "_id", Value: int32(1)}}) {
		t.Fatal("_id index must not be dropped")
	}
}
