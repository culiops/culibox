// Package awshost reads the host user's own AWS configuration: where the shared
// config files live and which profiles they define. It never executes anything
// a profile names and never reads the shared credentials file.
package awshost

import (
	"os"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// SharedFiles is where the SDK reads the shared config and credentials files
// from when AWS_CONFIG_FILE / AWS_SHARED_CREDENTIALS_FILE are set; nil means the
// SDK's home-directory default. Mint and SourceKey both resolve their files
// through it, so they can never disagree about which profile they mean.
// (LoadSharedConfigProfile, unlike LoadDefaultConfig, ignores those variables
// unless the paths are passed explicitly.)
func SharedFiles() (cfgs, creds []string) {
	if v := os.Getenv("AWS_CONFIG_FILE"); v != "" {
		cfgs = []string{v}
	}
	if v := os.Getenv("AWS_SHARED_CREDENTIALS_FILE"); v != "" {
		creds = []string{v}
	}
	return cfgs, creds
}

// LoadOpts are the LoadDefaultConfig options that pin SharedFiles.
func LoadOpts() []func(*awsconfig.LoadOptions) error {
	cfgs, creds := SharedFiles()
	var opts []func(*awsconfig.LoadOptions) error
	if cfgs != nil {
		opts = append(opts, awsconfig.WithSharedConfigFiles(cfgs))
	}
	if creds != nil {
		opts = append(opts, awsconfig.WithSharedCredentialsFiles(creds))
	}
	return opts
}
