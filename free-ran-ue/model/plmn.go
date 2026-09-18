package model

type TaiIE struct {
	Tac             string   `yaml:"tac" valid:"required"`
	BroadcastPlmnId PlmnIdIE `yaml:"broadcastPlmnId" valid:"required"`
}

type PlmnIdIE struct {
	Mcc string `yaml:"mcc" valid:"required"`
	Mnc string `yaml:"mnc" valid:"required"`
}
