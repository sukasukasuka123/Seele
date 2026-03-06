package util

import "github.com/spf13/viper"

// YAMLReading 通用 YAML 读取，返回 interface{}（动态配置场景）
func YAMLReading(configPath string) (interface{}, error) {
	var config interface{}
	viper.SetConfigFile(configPath)
	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}
	if err := viper.Unmarshal(&config); err != nil {
		return nil, err
	}
	return config, nil
}

// YAMLReadingInto 直接反序列化到目标结构体
func YAMLReadingInto[T any](configPath string) (T, error) {
	var target T
	viper.SetConfigFile(configPath)
	if err := viper.ReadInConfig(); err != nil {
		return target, err
	}
	if err := viper.Unmarshal(&target); err != nil {
		return target, err
	}
	return target, nil
}
