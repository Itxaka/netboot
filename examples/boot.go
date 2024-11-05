// Copyright 2024 Kairos contributors

package main

import (
	"fmt"
	"github.com/kairos-io/netboot/booters"
	"github.com/kairos-io/netboot/log"
	"github.com/kairos-io/netboot/server"
	"github.com/kairos-io/netboot/types"
	"os"
)

// This runs a quick server that serves iPXE and Debian netboot files.
func main() {
	var booterSpec *types.Spec
	if len(os.Args) < 2 {
		fmt.Println("Usage: boot <boot|efi>")
		os.Exit(1)
	}
	arg := os.Args[1]
	if arg == "boot" {
		version := "stable"
		arch := "amd64"
		mirror := "http://deb.debian.org/debian"

		kernel := fmt.Sprintf("%s/dists/%s/main/installer-%s/current/images/netboot/debian-installer/%s/linux", mirror, version, arch, arch)
		initrd := fmt.Sprintf("%s/dists/%s/main/installer-%s/current/images/netboot/debian-installer/%s/initrd.gz", mirror, version, arch, arch)
		booterSpec = &types.Spec{
			Kernel:  types.ID(kernel),
			Cmdline: "",
			Initrd:  []types.ID{types.ID(initrd)},
		}
	} else if arg == "efi" {
		booterSpec = &types.Spec{
			Efi: "https://boot.netboot.xyz/ipxe/netboot.xyz.efi",
		}
	} else {
		fmt.Println("Invalid argument")
		os.Exit(1)
	}
	log.SetDefaultLogger()

	ret := &server.Server{
		Log:        func(subsystem, msg string) { log.Log.Info().Str("subsystem", subsystem).Msgf(msg) },
		Debug:      func(subsystem, msg string) { log.Log.Debug().Str("subsystem", subsystem).Msgf(msg) },
		DHCPNoBind: true,
	}

	/*
		ret.PxeLinuxAssets = map[string]string{
			"default": "pxelinux.0",
		}

	*/

	ret.SetDefaultFirmwares()
	b, _ := booters.StaticBooter(booterSpec)
	ret.Booter = b
	err := ret.Serve()
	if err != nil {
		fmt.Println(err)
	}
}
