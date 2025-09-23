package config

import "os"

type Config struct {
	Address                    string
	StakeholdersServiceAddress string
	ToursServiceAddress        string
	BlogServiceAddress         string
	OrdersServiceAddress	   string
}

func GetConfig() Config {
	return Config{
		StakeholdersServiceAddress: os.Getenv("STAKEHOLDERS_SERVICE_ADDRESS"),
		BlogServiceAddress:         os.Getenv("BLOG_SERVICE_ADDRESS"),
		Address:                    os.Getenv("GATEWAY_ADDRESS"),
		ToursServiceAddress:        os.Getenv("TOURS_SERVICE_ADDRESS"),
		OrdersServiceAddress: 		os.Getenv("SHOPPING_CART_SERVICE_ADDRESS"),
	}
}
