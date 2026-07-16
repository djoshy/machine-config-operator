package main

import (
	"sort"
	"strings"

	bootimagemarketplace "github.com/openshift/machine-config-operator/pkg/controller/bootimage/marketplace"
)

// ProductSpec is a Marketplace product entry paired with the architecture whose installer
// ceiling it should be checked against.
type ProductSpec struct {
	ID, Name, Arch string
}

// allProductSpecs derives arch for each shared-package product entry by inspecting its name.
// Every entry encodes its architecture in the name except ROSA, which is x86_64-only.
func allProductSpecs() []ProductSpec {
	specs := make([]ProductSpec, 0, len(bootimagemarketplace.Products))
	for id, name := range bootimagemarketplace.Products {
		arch := "x86_64"
		if strings.Contains(name, "arm64") {
			arch = "arm64"
		}
		specs = append(specs, ProductSpec{ID: id, Name: name, Arch: arch})
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return specs
}
