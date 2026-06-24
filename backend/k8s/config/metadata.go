package config

import "embed"

//go:embed crd/bases/*
var crds embed.FS

func CrdFiles() embed.FS {
	return crds
}

//go:embed rbac/*
var rbac embed.FS

func RbacFiles() embed.FS {
	return rbac
}

//go:embed manager/*
var manager embed.FS

func ManagerFiles() embed.FS {
	return manager
}
