/*
Copyright © 2022 SiteWhere LLC - All Rights Reserved
Unauthorized copying of this file, via any medium is strictly prohibited.
Proprietary and confidential.
*/

package cmd

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	corev1beta1 "github.com/devicechain-io/dc-k8s/api/v1beta1"
	"github.com/fatih/color"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"

	"github.com/spf13/cobra"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/cli/values"
	"helm.sh/helm/v3/pkg/downloader"
	"helm.sh/helm/v3/pkg/getter"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
)

const (
	NS_DC_SYSTEM = "dc-system"
)

var (
	//go:embed install_infra/charts/*
	ChartFS embed.FS

	//go:embed install_infra/preinstall/*
	PreinstallFS embed.FS

	//go:embed install_infra/resources/*
	ResourcesFS embed.FS
)

// Create instance of install infra command
var installInfraCmd = NewInstallInfraCommand()

// Create command for installing DeviceChain infrastructure
func NewInstallInfraCommand() *cobra.Command {
	return &cobra.Command{
		Use:          "infra",
		Short:        "Install infrastructure components",
		Long:         `Installs and configures DeviceChain infrastructure dependencies`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return installInfraComponents()
		},
	}
}

// Install all infrastructure components
func installInfraComponents() error {
	fmt.Println("Preparing to install DeviceChain infrastructure components...")

	dynamicClient, discoveryClient, err := createClients()
	if err != nil {
		return err
	}

	// Validate that system namespace exists.
	err = assureSystemNamespace()
	if err != nil {
		return err
	}

	// Locate and/or setup Helm repositories.
	settings := cli.New()
	rfile, err := assureHelmRepositoryConfig(settings)
	if err != nil && !os.IsExist(err) {
		return err
	}

	// Add repositories required for infrastructure.
	entries := []*repo.Entry{
		{
			Name: "bitnami",
			URL:  "https://charts.bitnami.com/bitnami",
		},
		{
			Name: "timescale",
			URL:  "https://charts.timescale.com",
		},
		{
			Name: "mosquitto",
			URL:  "https://k8s-at-home.com/charts/",
		},
	}
	err = addHelmRepositories(entries, settings, rfile)
	if err != nil {
		return err
	}

	// Preinstall k8s resources from embedded yaml files.
	err = createPreinstallResources(dynamicClient, discoveryClient)
	if err != nil {
		return err
	}

	// Create Helm releases from embedded charts.
	err = createHelmReleases(settings)
	if err != nil {
		return err
	}

	// Create k8s resources from embedded yaml files.
	err = createInfraResources(dynamicClient, discoveryClient)
	if err != nil {
		return err
	}

	fmt.Println(color.HiGreenString("\nInstallation completed successfully."))
	return nil
}

// Assure that
func assureSystemNamespace() error {
	// Check for existing namespace.
	fmt.Print(color.WhiteString("\nVerifying DeviceChain system namespace... "))
	ns := &corev1.Namespace{}
	err := corev1beta1.V1Client.Get(context.Background(), types.NamespacedName{Name: NS_DC_SYSTEM}, ns)
	if err != nil {
		// Attempt to create the namespace.
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: NS_DC_SYSTEM}}
		err = corev1beta1.V1Client.Create(context.Background(), ns)
		fmt.Println(color.GreenString("Created system namespace."))
	} else {
		fmt.Println(color.GreenString("System namespace verified."))
	}
	return err
}

// Assure that Helm repository is initialized.
func assureHelmRepositoryConfig(settings *cli.EnvSettings) (*repo.File, error) {
	// Make sure Helm repository path exists
	err := os.MkdirAll(filepath.Dir(settings.RepositoryConfig), os.ModePerm)
	if err != nil && !os.IsExist(err) {
		return nil, err
	}

	// Get or create the repository file
	var file *repo.File
	if _, err := os.Stat(settings.RepositoryConfig); errors.Is(err, os.ErrNotExist) {
		file = repo.NewFile()
		err = file.WriteFile(settings.RepositoryConfig, 0755)
		if err != nil {
			return nil, err
		}
		fmt.Println(color.WhiteString("Created new Helm repositories configuration at: "),
			color.GreenString(settings.RepositoryConfig))
	} else {
		file, err = repo.LoadFile(settings.RepositoryConfig)
		if err != nil {
			return nil, err
		}
		fmt.Println(color.WhiteString("Using existing Helm repositories configuration at: "),
			color.GreenString(settings.RepositoryConfig))
	}
	return file, nil
}

// Add Helm repository to configuration
func addHelmRepositories(entries []*repo.Entry, settings *cli.EnvSettings, rfile *repo.File) error {
	for _, entry := range entries {
		fmt.Printf(color.WhiteString("Checking repository '%s' ... "), entry.Name)
		if rfile.Has(entry.Name) {
			fmt.Println(color.GreenString("FOUND"))
		} else {
			// Pull index file to verify..
			r, err := repo.NewChartRepository(entry, getter.All(settings))
			if err != nil {
				return err
			}
			_, err = r.DownloadIndexFile()
			if err != nil {
				return err
			}

			// Update repositories list.
			rfile.Update(entry)
			err = rfile.WriteFile(settings.RepositoryConfig, 0755)
			if err != nil {
				return err
			}
			fmt.Println(color.GreenString("ADDED"))
		}
	}
	return nil
}

// Log output used for helm debugging.
func helmDebug(format string, v ...interface{}) {
	fmt.Println(color.WhiteString(fmt.Sprintf(format, v...)))
}

type ChartInfo struct {
	Repository string
	Chart      string
	Version    string
	Release    string
}

// Determine whether chart is installable.
func isChartInstallable(ch *chart.Chart) (bool, error) {
	switch ch.Metadata.Type {
	case "", "application":
		return true, nil
	}
	return false, fmt.Errorf("%s charts are not installable", ch.Metadata.Type)
}

// Create a release for a Helm chart.
func createHelmRelease(settings *cli.EnvSettings, chart *ChartInfo, overrides []string) (*release.Release, error) {
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), NS_DC_SYSTEM, os.Getenv("HELM_DRIVER"), helmDebug); err != nil {
		return nil, err
	}
	installAction := action.NewInstall(actionConfig)
	installAction.Namespace = NS_DC_SYSTEM
	installAction.ReleaseName = chart.Release
	installAction.CreateNamespace = false
	installAction.SkipCRDs = true
	installAction.Wait = false
	installAction.Version = chart.Version

	// Locate path to chart.
	cp, err := installAction.ChartPathOptions.LocateChart(fmt.Sprintf("%s/%s", chart.Repository, chart.Chart), settings)
	if err != nil {
		return nil, err
	}

	// Create values that will be passed to chart.
	p := getter.All(settings)
	valueOpts := &values.Options{
		Values: overrides,
	}
	vals, err := valueOpts.MergeValues(p)
	if err != nil {
		return nil, err
	}

	// Load chart from established path.
	chartRequested, err := loader.Load(cp)
	if err != nil {
		return nil, err
	}

	// Verify chart is installable.
	validInstallableChart, err := isChartInstallable(chartRequested)
	if !validInstallableChart {
		return nil, err
	}

	// Download dependencies.
	if req := chartRequested.Metadata.Dependencies; req != nil {
		if err := action.CheckDependencies(chartRequested, req); err != nil {
			if installAction.DependencyUpdate {
				man := &downloader.Manager{
					Out:              os.Stdout,
					ChartPath:        cp,
					Keyring:          installAction.ChartPathOptions.Keyring,
					SkipUpdate:       false,
					Getters:          p,
					RepositoryConfig: settings.RepositoryConfig,
					RepositoryCache:  settings.RepositoryCache,
				}
				if err := man.Update(); err != nil {
					return nil, err
				}
			} else {
				return nil, err
			}
		}
	}

	// Run the action to create a release.
	release, err := installAction.Run(chartRequested, vals)
	if err != nil {
		return nil, err
	}

	return release, nil
}

// Uninstall a Helm release.
func uninstallHelmRelease(settings *cli.EnvSettings, chart *ChartInfo) (*release.UninstallReleaseResponse, error) {
	actionConfig := new(action.Configuration)
	if err := actionConfig.Init(settings.RESTClientGetter(), NS_DC_SYSTEM, os.Getenv("HELM_DRIVER"), helmDebug); err != nil {
		return nil, err
	}
	uninstallAction := action.NewUninstall(actionConfig)
	return uninstallAction.Run(chart.Release)
}

// Parse a filename into chart info.
func parseChartInfo(d fs.DirEntry) (*ChartInfo, error) {
	parts := strings.Split(strings.TrimSuffix(d.Name(), ".properties"), "_")
	if len(parts) != 4 {
		return nil, errors.New("chart filename must have exactly 4 parts separated by underscores")
	}
	cinfo := &ChartInfo{
		Repository: parts[1],
		Chart:      parts[2],
		Version:    parts[3],
		Release:    "dc-" + parts[2],
	}
	return cinfo, nil
}

// Create Helm releases for each chart embedded in the binary.
func createHelmReleases(settings *cli.EnvSettings) error {
	fmt.Println(GreenUnderline("\nInstall Helm Charts"))
	return fs.WalkDir(ChartFS, "install_infra/charts", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			cinfo, err := parseChartInfo(d)
			if err != nil {
				return err
			}
			fmt.Printf("Installing Helm Chart: Repository: %s Chart: %s Version: %s Release: %s\n",
				color.GreenString(cinfo.Repository),
				color.GreenString(cinfo.Chart),
				color.GreenString(cinfo.Version),
				color.GreenString(cinfo.Release),
			)

			// Read list of overrides from file.
			file, err := ChartFS.Open(path)
			if err != nil {
				return err
			}
			bytes, err := io.ReadAll(file)
			if err != nil {
				return err
			}
			overrides := make([]string, 0)
			lines := strings.Split(string(bytes), "\n")
			for _, line := range lines {
				overrides = append(overrides, strings.TrimSpace(line))
			}

			uninstallHelmRelease(settings, cinfo)
			_, err = createHelmRelease(settings, cinfo, overrides)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// Preinstall (before helm charts) k8s resources for each yaml file embedded in the binary.
func createPreinstallResources(dynamicClient dynamic.Interface, discoveryClient *discovery.DiscoveryClient) error {
	fmt.Println(GreenUnderline("\nPreinstall Infra Resources"))
	caser := cases.Title(language.Und)
	return fs.WalkDir(PreinstallFS, "install_infra/preinstall", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			parts := strings.Split(strings.TrimSuffix(d.Name(), ".yaml"), "_")
			pname := caser.String(strings.ReplaceAll(strings.ToLower(parts[1]), "-", " "))
			fmt.Printf("Preinstalling Yaml Resource: %s\n", color.GreenString(pname))

			file, err := PreinstallFS.Open(path)
			if err != nil {
				return err
			}
			bytes, err := io.ReadAll(file)
			if err != nil {
				return err
			}

			err = applyYaml(dynamicClient, discoveryClient, bytes)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// Create k8s resources for each yaml file embedded in the binary.
func createInfraResources(dynamicClient dynamic.Interface, discoveryClient *discovery.DiscoveryClient) error {
	fmt.Println(GreenUnderline("\nInstall Infra Resources"))
	caser := cases.Title(language.Und)
	return fs.WalkDir(ResourcesFS, "install_infra/resources", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			parts := strings.Split(strings.TrimSuffix(d.Name(), ".yaml"), "_")
			pname := caser.String(strings.ReplaceAll(strings.ToLower(parts[1]), "-", " "))
			fmt.Printf("Installing Yaml Resource: %s\n", color.GreenString(pname))

			file, err := ResourcesFS.Open(path)
			if err != nil {
				return err
			}
			bytes, err := io.ReadAll(file)
			if err != nil {
				return err
			}

			err = applyYaml(dynamicClient, discoveryClient, bytes)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func init() {
	installCmd.AddCommand(installInfraCmd)
}
