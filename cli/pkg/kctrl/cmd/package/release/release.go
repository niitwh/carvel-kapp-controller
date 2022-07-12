// Copyright 2022 VMware, Inc.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cppforlife/go-cli-ui/ui"
	"github.com/spf13/cobra"
	"github.com/vmware-tanzu/carvel-kapp-controller/cli/pkg/kctrl/cmd/app/init/appbuild"
	cmdcore "github.com/vmware-tanzu/carvel-kapp-controller/cli/pkg/kctrl/cmd/core"
	cmdpkgbuild "github.com/vmware-tanzu/carvel-kapp-controller/cli/pkg/kctrl/cmd/package/init"
	cmdlocal "github.com/vmware-tanzu/carvel-kapp-controller/cli/pkg/kctrl/local"
	"github.com/vmware-tanzu/carvel-kapp-controller/cli/pkg/kctrl/logger"
	kcv1alpha1 "github.com/vmware-tanzu/carvel-kapp-controller/pkg/apis/kappctrl/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type ReleaseOptions struct {
	ui          cmdcore.AuthoringUI
	depsFactory cmdcore.DepsFactory
	logger      logger.Logger

	pkgVersion     string
	chdir          string
	outputLocation string
	debug          bool
}

const (
	defaultArtefactDir = "carvel-artefacts"
	lockOutputFolder   = ".imgpkg"
	defaultVersion     = "0.0.0-%d"
)

func NewReleaseOptions(ui ui.UI, depsFactory cmdcore.DepsFactory, logger logger.Logger) *ReleaseOptions {
	return &ReleaseOptions{ui: cmdcore.NewAuthoringUIImpl(ui), depsFactory: depsFactory, logger: logger}
}

func NewReleaseCmd(o *ReleaseOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "release",
		Short: "Release package",
		RunE:  func(cmd *cobra.Command, args []string) error { return o.Run() },
	}

	cmd.Flags().StringVarP(&o.pkgVersion, "version", "v", "", "Version to be released")
	cmd.Flags().StringVar(&o.chdir, "chdir", "", "Working directory with package-build and other config")
	cmd.Flags().StringVar(&o.outputLocation, "repo-output", defaultArtefactDir, "Output location for artefacts")
	cmd.Flags().BoolVar(&o.debug, "debug", false, "Version to be released")

	return cmd
}

func (o *ReleaseOptions) Run() error {
	if o.pkgVersion == "" {
		o.pkgVersion = fmt.Sprintf(defaultVersion, time.Now().Unix())
	}

	if o.chdir != "" {
		err := os.Chdir(o.chdir)
		if err != nil {
			return err
		}
	}

	wd, err := os.Getwd()
	if err != nil {
		return err
	}

	pkgBuild, err := cmdpkgbuild.NewPackageBuildFromFile(filepath.Join(wd, "package-build.yml"))
	if err != nil {
		return err
	}

	err = o.loadExportData(pkgBuild)
	if err != nil {
		return err
	}

	// In-memory app for building and pushing images
	builderApp := kcv1alpha1.App{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kctrl-builder",
			Namespace: "in-memory",
			Annotations: map[string]string{
				"kctrl.carvel.dev/local-fetch-0": ".",
			},
		},
		Spec: kcv1alpha1.AppSpec{
			Fetch: []kcv1alpha1.AppFetch{
				{
					// To be replaced by local fetch
					Git: &kcv1alpha1.AppFetchGit{},
				},
			},
			Template: pkgBuild.Spec.Template.Spec.App.Spec.Template,
			Deploy:   pkgBuild.Spec.Template.Spec.App.Spec.Deploy,
		},
	}
	buildConfigs := cmdlocal.Configs{
		Apps: []kcv1alpha1.App{builderApp},
	}

	o.printPrerequisites()

	// Create temporary directory for imgpkg lock file
	err = os.Mkdir(filepath.Join(wd, lockOutputFolder), os.ModePerm)
	if err != nil {
		return err
	}
	defer os.RemoveAll(filepath.Join(wd, lockOutputFolder))

	imgpkgLockPath := filepath.Join(wd, lockOutputFolder, "images.yml")
	cmdRunner := NewReleaseCmdRunner(os.Stdout, o.debug, imgpkgLockPath, o.ui)
	reconciler := cmdlocal.NewReconciler(o.depsFactory, cmdRunner, o.logger)

	err = reconciler.Reconcile(buildConfigs, cmdlocal.ReconcileOpts{
		Local:     true,
		KbldBuild: true,
	})

	var imgpkgBundleURL string
	useKbldImagesLock := false
	for _, exportStep := range pkgBuild.Spec.Template.Spec.Export {
		switch {
		case exportStep.ImgpkgBundle != nil:
			useKbldImagesLock = exportStep.ImgpkgBundle.UseKbldImagesLock
			imgpkgOutput, err := ImgpkgRunner{
				Image:             exportStep.ImgpkgBundle.Image,
				Version:           o.pkgVersion,
				Paths:             exportStep.IncludePaths,
				UseKbldImagesLock: useKbldImagesLock,
				ImgLockFilepath:   imgpkgLockPath,
				UI:                o.ui,
			}.Run()
			if err != nil {
				return err
			}
			imgpkgBundleURL, err = o.imgpkgBundleURLFromStdout(imgpkgOutput)
			if err != nil {
				return err
			}
		default:
			continue
		}
	}

	artefactWriter := NewArtefactWriter(pkgBuild.ObjectMeta.Name, o.pkgVersion, o.outputLocation, o.ui)
	err = artefactWriter.CreatePackageDir()
	if err != nil {
		return err
	}
	err = artefactWriter.TouchPackageMetadata()
	if err != nil {
		return err
	}
	err = artefactWriter.WritePackageFile(imgpkgBundleURL, pkgBuild.Spec.Template.Spec.App.Spec, useKbldImagesLock)
	if err != nil {
		return err
	}

	o.printNextSteps()
	return nil
}

func (o *ReleaseOptions) loadExportData(pkgBuild *cmdpkgbuild.PackageBuild) error {
	if len(pkgBuild.Spec.Template.Spec.Export) == 0 {
		pkgBuild.Spec.Template.Spec.Export = []appbuild.Export{
			{
				ImgpkgBundle: &appbuild.ImgpkgBundle{
					UseKbldImagesLock: true,
				},
			},
		}
	}
	if pkgBuild.Spec.Template.Spec.Export[0].ImgpkgBundle == nil {
		pkgBuild.Spec.Template.Spec.Export[0].ImgpkgBundle = &appbuild.ImgpkgBundle{
			UseKbldImagesLock: true,
		}
	}
	defaultImgValue := pkgBuild.Spec.Template.Spec.Export[0].ImgpkgBundle.Image
	o.ui.PrintInformationalText("The bundle created needs to be pushed to an OCI registry. Registry URL format: <REGISTRY_URL/REPOSITORY_NAME> e.g. index.docker.io/k8slt/sample-bundle")
	textOpts := ui.TextOpts{
		Label:        "Enter the registry URL",
		Default:      defaultImgValue,
		ValidateFunc: nil,
	}
	imgValue, err := o.ui.AskForText(textOpts)
	if err != nil {
		return err
	}
	pkgBuild.Spec.Template.Spec.Export[0].ImgpkgBundle.Image = strings.TrimSpace(imgValue)
	return pkgBuild.Save()
}

func (o *ReleaseOptions) imgpkgBundleURLFromStdout(imgpkgStdout string) (string, error) {
	lines := strings.Split(imgpkgStdout, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "Pushed") {
			line = strings.TrimPrefix(line, "Pushed")
			line = strings.Replace(line, "'", "", -1)
			line = strings.Replace(line, " ", "", -1)
			return line, nil
		}
	}
	return "", fmt.Errorf("Could not get imgpkg bundle location")
}

func (o *ReleaseOptions) printPrerequisites() {
	o.ui.PrintHeaderText("Pre-requisites")
	o.ui.PrintInformationalText("1. The host must be authorised to push images to a registry (can be set up by running `docker login`)\n" +
		"2. Package can be released with this command only once `kctrl package init` has been run successfully.\n")
}

func (o *ReleaseOptions) printNextSteps() {
	o.ui.PrintHeaderText("\nNext steps")
	o.ui.PrintInformationalText("1. The artefacts generated by the `--repo-output` flag can be bundled into a repository using the `kctrl package repo release` comand.\n" +
		"2. Package and PackageMetadata YAML generated can be applied to the cluster directly so that it can be installed.")
}
