package config

import "os"

type Config struct {
	Address                    string
	StakeholdersServiceAddress string
}

func GetConfig() Config {
	return Config{
		StakeholdersServiceAddress: os.Getenv("STAKEHOLDERS_SERVICE_ADDRESS"),
		Address:                    os.Getenv("GATEWAY_ADDRESS"),
	}
}
