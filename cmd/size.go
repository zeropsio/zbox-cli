package cmd

import (
	"github.com/spf13/cobra"
)

// sizeFlags are --cpus, --memory and --disk of the commands that boot a sandbox at a size. An omitted flag keeps the size the command starts from.
type sizeFlags struct {
	cpus   int
	memory string
	disk   string
}

func (f *sizeFlags) add(cmd *cobra.Command, from string) {
	cmd.Flags().IntVar(&f.cpus, "cpus", 0, "cpu count, "+from+" when omitted")
	cmd.Flags().StringVar(&f.memory, "memory", "", "memory cap, for example 512m or 2g, a multiple of 16m from 64m up; "+from+" when omitted")
	cmd.Flags().StringVar(&f.disk, "disk", "", "disk size, for example 4g, a whole number of MiB from 1g up to the workspace's volume; "+from+" when omitted")
}

func (f *sizeFlags) given() bool {
	return f.cpus != 0 || f.memory != "" || f.disk != ""
}

// values answers the flags as the API's optional fields.
func (f *sizeFlags) values() (*int, *int64, *int64, error) {
	memory, err := optionalBytes(f.memory)
	if err != nil {
		return nil, nil, nil, err
	}
	disk, err := optionalBytes(f.disk)
	if err != nil {
		return nil, nil, nil, err
	}
	return optionalInt(f.cpus), memory, disk, nil
}

func optionalBytes(s string) (*int64, error) {
	if s == "" {
		return nil, nil
	}
	n, err := parseBytes(s)
	if err != nil {
		return nil, err
	}
	return &n, nil
}
