package jas

import (
	"github.com/jfrog/jfrog-cli-core/v2/utils/coreutils"
	"github.com/jfrog/jfrog-client-go/utils/log"
	"github.com/jfrog/jfrog-client-go/xray/services"
	"github.com/owenrumney/go-sarif/sarif"
	"gopkg.in/yaml.v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	applicabilityScanCommand = "ca"
	applicabilityScanType    = "analyze-applicability"
)

var (
	analyzerManagerExecuter AnalyzerManager = &analyzerManager{}
)

type ExtendedScanResults struct {
	XrayResults                 []services.ScanResponse
	ApplicabilityScannerResults map[string]string
	EntitledForJas              bool
}

func (e *ExtendedScanResults) GetXrayScanResults() []services.ScanResponse {
	return e.XrayResults
}

func GetExtendedScanResults(results []services.ScanResponse) (*ExtendedScanResults, error) {
	applicabilityScanManager := NewApplicabilityScanManager(results)
	if !applicabilityScanManager.analyzerManager.DoesAnalyzerManagerExecutableExist() {
		log.Info("user not entitled for jas, didnt execute applicability scan")
		return &ExtendedScanResults{XrayResults: results, ApplicabilityScannerResults: nil, EntitledForJas: false}, nil
	}
	err := applicabilityScanManager.Run()
	if err != nil {
		return handleApplicabilityScanError(err, applicabilityScanManager)
	}
	applicabilityScanResults := applicabilityScanManager.GetApplicabilityScanResults()
	extendedScanResults := ExtendedScanResults{XrayResults: results, ApplicabilityScannerResults: applicabilityScanResults, EntitledForJas: true}
	return &extendedScanResults, nil
}

func handleApplicabilityScanError(err error, applicabilityScanManager *ApplicabilityScanManager) (*ExtendedScanResults, error) {
	log.Info("failed to run applicability scan: " + err.Error())
	deleteFilesError := applicabilityScanManager.DeleteApplicabilityScanProcessFiles()
	if deleteFilesError != nil {
		return nil, deleteFilesError
	}
	return nil, err
}

type ApplicabilityScanManager struct {
	applicabilityScannerResults map[string]string
	xrayVulnerabilities         []services.Vulnerability
	configFileName              string
	resultsFileName             string
	analyzerManager             AnalyzerManager
}

func NewApplicabilityScanManager(xrayScanResults []services.ScanResponse) *ApplicabilityScanManager {
	xrayVulnerabilities := getXrayVulnerabilities(xrayScanResults)
	return &ApplicabilityScanManager{
		applicabilityScannerResults: map[string]string{},
		xrayVulnerabilities:         xrayVulnerabilities,
		configFileName:              generateRandomFileName() + ".yaml",
		resultsFileName:             "sarif.sarif", //generateRandomFileName() + ".sarif",
		analyzerManager:             analyzerManagerExecuter,
	}
}

func getXrayVulnerabilities(xrayScanResults []services.ScanResponse) []services.Vulnerability {
	xrayVulnerabilities := []services.Vulnerability{}
	for _, result := range xrayScanResults {
		for _, vul := range result.Vulnerabilities {
			xrayVulnerabilities = append(xrayVulnerabilities, vul)
		}
	}
	return xrayVulnerabilities
}

func (a *ApplicabilityScanManager) GetApplicabilityScanResults() map[string]string {
	return a.applicabilityScannerResults
}

func (a *ApplicabilityScanManager) Run() error {
	if err := a.createConfigFile(); err != nil {
		return err
	}
	if err := a.runAnalyzerManager(); err != nil {
		return err
	}
	if err := a.parseResults(); err != nil {
		return err
	}
	if err := a.DeleteApplicabilityScanProcessFiles(); err != nil {
		return err
	}
	return nil
}

type applicabilityScanConfig struct {
	Scans []scanConfiguration `yaml:"scans"`
}

type scanConfiguration struct {
	Roots          []string `yaml:"roots"`
	Output         string   `yaml:"output"`
	Type           string   `yaml:"type"`
	GrepDisable    bool     `yaml:"grep-disable"`
	CveWhitelist   []string `yaml:"cve-whitelist"`
	SkippedFolders []string `yaml:"skipped-folders"`
}

func (a *ApplicabilityScanManager) createConfigFile() error {
	currentDir, err := coreutils.GetWorkingDirectory()
	if err != nil {
		return err
	}
	configFileContent := applicabilityScanConfig{
		Scans: []scanConfiguration{
			{
				Roots:          []string{currentDir},
				Output:         filepath.Join(currentDir, a.resultsFileName),
				Type:           applicabilityScanType,
				GrepDisable:    false,
				CveWhitelist:   a.createCveList(),
				SkippedFolders: []string{},
			},
		},
	}
	yamlData, err := yaml.Marshal(&configFileContent)
	if err != nil {
		return err
	}
	err = os.WriteFile(a.configFileName, yamlData, 0644)
	if err != nil {
		return err
	}
	return nil
}

func (a *ApplicabilityScanManager) runAnalyzerManager() error {
	currentDir, err := coreutils.GetWorkingDirectory()
	if err != nil {
		return err
	}
	err = a.analyzerManager.RunAnalyzerManager(filepath.Join(currentDir, a.configFileName))
	if err != nil {
		return err
	}
	return nil
}

func (a *ApplicabilityScanManager) parseResults() error {
	report, err := sarif.Open(a.resultsFileName)
	if err != nil {
		return err
	}
	var fullVulnerabilitiesList []*sarif.Result
	if len(report.Runs) > 0 {
		fullVulnerabilitiesList = report.Runs[0].Results
	}

	xrayCves := a.createCveList()
	for _, xrayCve := range xrayCves {
		a.applicabilityScannerResults[xrayCve] = "unknown"
	}

	for _, vulnerability := range fullVulnerabilitiesList {
		applicableVulnerabilityName := getVulnerabilityName(*vulnerability.RuleID)
		if isVulnerabilityApplicable(vulnerability) {
			a.applicabilityScannerResults[applicableVulnerabilityName] = "Yes"
		} else {
			a.applicabilityScannerResults[applicableVulnerabilityName] = "No"
		}
	}
	return nil
}

func (a *ApplicabilityScanManager) DeleteApplicabilityScanProcessFiles() error {
	err := os.Remove(a.configFileName)
	if err != nil {
		return err
	}
	err = os.Remove(a.resultsFileName)
	if err != nil {
		return err
	}
	return nil
}

func (a *ApplicabilityScanManager) createCveList() []string {
	cveWhiteList := []string{}
	for _, vulnerability := range a.xrayVulnerabilities {
		for _, cve := range vulnerability.Cves {
			if cve.Id != "" {
				cveWhiteList = append(cveWhiteList, cve.Id)
			}
		}
	}
	return cveWhiteList
}

func isVulnerabilityApplicable(vulnerability *sarif.Result) bool {
	return !(vulnerability.Kind != nil && *vulnerability.Kind == "pass")
}

func getVulnerabilityName(sarifRuleId string) string {
	return strings.Split(sarifRuleId, "_")[1]
}

type AnalyzerManager interface {
	DoesAnalyzerManagerExecutableExist() bool
	RunAnalyzerManager(string) error
}

type analyzerManager struct {
}

func (am *analyzerManager) DoesAnalyzerManagerExecutableExist() bool {
	if _, err := os.Stat(analyzerManagerFilePath); err != nil {
		return false
	}
	return true
}

func (am *analyzerManager) RunAnalyzerManager(configFile string) error {
	_, err := exec.Command(analyzerManagerFilePath, applicabilityScanCommand, configFile).Output()
	if err != nil {
		return err
	}
	return nil
}