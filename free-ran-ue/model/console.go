package model

import "time"

type ConsoleConfig struct {
	Console ConsoleIE `yaml:"console" valid:"required"`
	Logger  LoggerIE  `yaml:"logger" valid:"required"`
}

type ConsoleIE struct {
	Username string `yaml:"username" valid:"required"`
	Password string `yaml:"password" valid:"required"`

	Port int `yaml:"port" valid:"required"`

	JWT JWTIE `yaml:"jwt" valid:"required"`

	FrontendFilePath string `yaml:"frontendFilePath" valid:"required"`
}

type JWTIE struct {
	Secret    string        `yaml:"secret" valid:"required"`
	ExpiresIn time.Duration `yaml:"expiresIn" valid:"required"`
}
