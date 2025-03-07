package envsetup

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jfrog/jfrog-cli-core/v2/artifactory/commands/generic"
	"github.com/jfrog/jfrog-cli-core/v2/artifactory/utils"
	"github.com/jfrog/jfrog-cli-core/v2/utils/ioutils"
	"github.com/jfrog/jfrog-client-go/access/services"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkg/browser"

	"github.com/google/uuid"
	"github.com/jfrog/jfrog-cli-core/v2/common/commands"
	"github.com/jfrog/jfrog-cli-core/v2/utils/config"
	"github.com/jfrog/jfrog-cli-core/v2/utils/coreutils"
	"github.com/jfrog/jfrog-client-go/http/httpclient"
	clientutils "github.com/jfrog/jfrog-client-go/utils"
	"github.com/jfrog/jfrog-client-go/utils/errorutils"
	clientioutils "github.com/jfrog/jfrog-client-go/utils/io"
	"github.com/jfrog/jfrog-client-go/utils/io/httputils"
	"github.com/jfrog/jfrog-client-go/utils/log"
)

type OutputFormat string

const (
	myJfrogEndPoint      = "https://myjfrog-api.jfrog.com/api/v1/activation/cloud/cli/getStatus/"
	syncSleepInterval    = 5 * time.Second  // 5 seconds
	maxWaitMinutes       = 30 * time.Minute // 30 minutes
	nonExpiredTokenValue = 0                // Access Tokens with 0 expiration value are actually generated by Access with 1 year expiration.

	// OutputFormat values
	Human   OutputFormat = "human"
	Machine OutputFormat = "machine"

	// When entering password on terminal the user has limited number of retries.
	enterPasswordMaxRetries = 20

	MessageIdes = "📦 If you're using VS Code, IntelliJ IDEA, WebStorm, PyCharm, Android Studio or GoLand\n" +
		"   Open the IDE 👉 Install the JFrog extension or plugin 👉 View the JFrog panel"
	MessageDockerDesktop = "📦 Open Docker Desktop and install the JFrog Extension to scan any of your \n" +
		"   local docker images"
	MessageDockerScan = "📦 Scan local Docker images from the terminal by running\n" +
		"   jf docker scan <image name>:<image tag>"
)

var trueValue = true

type EnvSetupCommand struct {
	registrationURL string
	// In case encodedConnectionDetails were provided - we have a registered user that was invited to the platform.
	encodedConnectionDetails string
	id                       uuid.UUID
	serverDetails            *config.ServerDetails
	progress                 clientioutils.ProgressMgr
	outputFormat             OutputFormat
}

func (ftc *EnvSetupCommand) SetRegistrationURL(registrationURL string) *EnvSetupCommand {
	ftc.registrationURL = registrationURL
	return ftc
}

func (ftc *EnvSetupCommand) SetEncodedConnectionDetails(encodedConnectionDetails string) *EnvSetupCommand {
	ftc.encodedConnectionDetails = encodedConnectionDetails
	return ftc
}

func (ftc *EnvSetupCommand) ServerDetails() (*config.ServerDetails, error) {
	return nil, nil
}

func (ftc *EnvSetupCommand) SetProgress(progress clientioutils.ProgressMgr) {
	ftc.progress = progress
}

func (ftc *EnvSetupCommand) SetOutputFormat(format OutputFormat) *EnvSetupCommand {
	ftc.outputFormat = format
	return ftc
}

func NewEnvSetupCommand() *EnvSetupCommand {
	return &EnvSetupCommand{
		id: uuid.New(),
	}
}

// This function is a wrapper around the 'ftc.progress.SetHeadlineMsg(msg)' API,
// to make sure that ftc.progress isn't nil. It can be nil in case the CI environment variable is set.
// In case ftc.progress is nil, the message sent will be prompted to the screen
// without the progress indication.
func (ftc *EnvSetupCommand) setHeadlineMsg(msg string) {
	if ftc.progress != nil {
		ftc.progress.SetHeadlineMsg(msg)
	} else {
		log.Output(msg + "...")
	}
}

// This function is a wrapper around the 'ftc.progress.clearHeadlineMsg()' API,
// to make sure that ftc.progress isn't nil before clearing it.
// It can be nil in case the CI environment variable is set.
func (ftc *EnvSetupCommand) clearHeadlineMsg() {
	if ftc.progress != nil {
		ftc.progress.ClearHeadlineMsg()
	}
}

// This function is a wrapper around the 'ftc.progress.Quit()' API,
// to make sure that ftc.progress isn't nil before clearing it.
// It can be nil in case the CI environment variable is set.
func (ftc *EnvSetupCommand) quitProgress() error {
	if ftc.progress != nil {
		return ftc.progress.Quit()
	}
	return nil
}

func (ftc *EnvSetupCommand) Run() (err error) {
	err = ftc.SetupAndConfigServer()
	if err != nil {
		return
	}
	if ftc.outputFormat == Human {
		// Closes the progress manger and reset the log prints.
		err = ftc.quitProgress()
		if err != nil {
			return
		}
		log.Output()
		log.Output(coreutils.PrintBold("Congrats! You're all set"))
		log.Output("So what's next?")
		message :=
			coreutils.PrintTitle("IDE") + "\n" +
				MessageIdes + "\n\n" +
				coreutils.PrintTitle("Docker") + "\n" +
				"You can scan your local Docker images from the terminal or the Docker Desktop UI\n" +
				MessageDockerScan + "\n" +
				MessageDockerDesktop + "\n\n" +
				coreutils.PrintTitle("Build, scan & deploy") + "\n" +
				"1. 'cd' into your code project directory\n" +
				"2. Run 'jf project init'\n\n" +
				coreutils.PrintTitle("Read more") + "\n" +
				"📦 Read more about how to get started at -\n" +
				"   " + coreutils.PrintLink(coreutils.GettingStartedGuideUrl)
		err = coreutils.PrintTable("", "", message, false)
		if err != nil {
			return
		}
		if ftc.encodedConnectionDetails == "" {
			log.Output(coreutils.PrintBold("Important"))
			log.Output("Please use the email we've just sent you, to verify your email address during the next 48 hours.\n")
		}
	}
	return
}

func (ftc *EnvSetupCommand) SetupAndConfigServer() (err error) {
	var server *config.ServerDetails
	// If credentials were provided, this means that the user was invited to join an existing JFrog environment.
	// Otherwise, this is a brand-new user, that needs to register and set up a new JFrog environment.
	if ftc.encodedConnectionDetails == "" {
		server, err = ftc.setupNewUser()
	} else {
		server, err = ftc.setupExistingUser()
	}
	if err != nil {
		return
	}
	err = configServer(server)
	return
}

func (ftc *EnvSetupCommand) setupNewUser() (server *config.ServerDetails, err error) {
	if ftc.outputFormat == Human {
		ftc.setHeadlineMsg("Just fill out its details in your browser 📝")
		time.Sleep(8 * time.Second)
	} else {
		// Closes the progress manger and reset the log prints.
		err = ftc.quitProgress()
		if err != nil {
			return
		}
	}
	err = browser.OpenURL(ftc.registrationURL + "?id=" + ftc.id.String())
	if err != nil {
		return
	}
	server, err = ftc.getNewServerDetails()
	return
}

func (ftc *EnvSetupCommand) setupExistingUser() (server *config.ServerDetails, err error) {
	err = ftc.quitProgress()
	if err != nil {
		return
	}
	server, err = ftc.decodeConnectionDetails()
	if err != nil {
		return
	}
	if server.Url == "" {
		err = errorutils.CheckErrorf("The response from JFrog Access does not include a JFrog environment URL")
		return
	}
	if server.AccessToken != "" {
		// If the server details received from JFrog Access include an access token, this access token is
		// short-lived, and we therefore need to replace it with a new long-lived access token, and configure
		// JFrog CLI with it.
		err = GenerateNewLongTermRefreshableAccessToken(server)
		return
	}
	if server.User == "" {
		err = errorutils.CheckErrorf("The response from JFrog Access does not includes a username or access token")
		return
	}
	// Url and accessToken/userName must be provided in the base64 encoded connection details.
	// APIkey/password are optional - In case they were not provided user can enter his password on console.
	// Password will be validated before the config command is being called.
	if server.Password == "" {
		err = ftc.scanAndValidateJFrogPasswordFromConsole(server)
	}
	return
}

func (ftc *EnvSetupCommand) scanAndValidateJFrogPasswordFromConsole(server *config.ServerDetails) (err error) {
	// User has limited number of retries to enter his correct password.
	// Password validation is operated by Artifactory ping API.
	server.ArtifactoryUrl = clientutils.AddTrailingSlashIfNeeded(server.Url) + "artifactory/"
	for i := 0; i < enterPasswordMaxRetries; i++ {
		server.Password, err = ioutils.ScanJFrogPasswordFromConsole()
		if err != nil {
			return
		}
		// Validate correct password by using Artifactory ping API.
		pingCmd := generic.NewPingCommand().SetServerDetails(server)
		err = commands.Exec(pingCmd)
		if err == nil {
			// No error while encrypting password => correct password.
			return
		}
		log.Output(err.Error())
	}
	err = errorutils.CheckError(errors.New("bad credentials: Wrong password. "))
	return
}

// Take the short-lived token and generate a long term (1 year expiry) refreshable accessToken.
func GenerateNewLongTermRefreshableAccessToken(server *config.ServerDetails) (err error) {
	accessManager, err := utils.CreateAccessServiceManager(server, false)
	if err != nil {
		return
	}
	// Create refreshable accessToken with 1 year expiry from the given short expiry token.
	params := createLongExpirationRefreshableTokenParams()
	token, err := accessManager.CreateAccessToken(*params)
	if err != nil {
		return
	}
	server.AccessToken = token.AccessToken
	server.RefreshToken = token.RefreshToken
	return
}

func createLongExpirationRefreshableTokenParams() *services.CreateTokenParams {
	params := services.CreateTokenParams{}
	params.ExpiresIn = nonExpiredTokenValue
	params.Refreshable = &trueValue
	params.Audience = "*@*"
	return &params
}

func (ftc *EnvSetupCommand) decodeConnectionDetails() (server *config.ServerDetails, err error) {
	rawDecodedText, err := base64.StdEncoding.DecodeString(ftc.encodedConnectionDetails)
	if errorutils.CheckError(err) != nil {
		return
	}
	err = json.Unmarshal(rawDecodedText, &server)
	if errorutils.CheckError(err) != nil {
		return nil, err
	}
	return
}

func (ftc *EnvSetupCommand) CommandName() string {
	return "setup"
}

// Returns the new server deatailes from My-JFrog
func (ftc *EnvSetupCommand) getNewServerDetails() (serverDetails *config.ServerDetails, err error) {
	requestBody := &myJfrogGetStatusRequest{CliRegistrationId: ftc.id.String()}
	requestContent, err := json.Marshal(requestBody)
	if errorutils.CheckError(err) != nil {
		return nil, err
	}

	httpClientDetails := httputils.HttpClientDetails{
		Headers: map[string]string{"Content-Type": "application/json"},
	}
	client, err := httpclient.ClientBuilder().Build()
	if err != nil {
		return nil, err
	}

	// Define the MyJFrog polling logic.
	pollingMessage := fmt.Sprintf("Sync: Get MyJFrog status report. Request ID:%s...", ftc.id)
	pollingErrorMessage := "Sync: Get MyJFrog status request failed. Attempt: %d. Error: %s"
	// The max consecutive polling errors allowed, before completely failing the setup action.
	const maxConsecutiveErrors = 6
	errorsCount := 0
	readyMessageDisplayed := false
	pollingAction := func() (shouldStop bool, responseBody []byte, err error) {
		log.Debug(pollingMessage)
		// Send request to MyJFrog.
		resp, body, err := client.SendPost(myJfrogEndPoint, requestContent, httpClientDetails, "")
		// If an HTTP error occurred.
		if err != nil {
			errorsCount++
			log.Debug(fmt.Sprintf(pollingErrorMessage, errorsCount, err.Error()))
			if errorsCount == maxConsecutiveErrors {
				return true, nil, err
			}
			return false, nil, nil
		}
		// If the response is not the expected 200 or 404.
		if err = errorutils.CheckResponseStatusWithBody(resp, body, http.StatusOK, http.StatusNotFound); err != nil {
			errorsCount++
			log.Debug(fmt.Sprintf(pollingErrorMessage, errorsCount, err.Error()))
			if errorsCount == maxConsecutiveErrors {
				return true, nil, err
			}
			return false, nil, nil
		}
		errorsCount = 0

		// Wait for 'ready=true' response from MyJFrog
		if resp.StatusCode == http.StatusOK {
			if !readyMessageDisplayed {
				if ftc.outputFormat == Machine {
					log.Output("PREPARING_ENV")
				} else {
					ftc.clearHeadlineMsg()
					ftc.setHeadlineMsg("Almost done! Please hang on while JFrog CLI completes the setup 🛠")
				}
				readyMessageDisplayed = true
			}
			statusResponse := myJfrogGetStatusResponse{}
			if err = json.Unmarshal(body, &statusResponse); err != nil {
				return true, nil, err
			}
			// Got the new server details
			if statusResponse.Ready {
				return true, body, nil
			}
		}
		// The expected 404 response or 200 response without 'Ready'
		return false, nil, nil
	}

	pollingExecutor := &httputils.PollingExecutor{
		Timeout:         maxWaitMinutes,
		PollingInterval: syncSleepInterval,
		PollingAction:   pollingAction,
	}

	body, err := pollingExecutor.Execute()
	if err != nil {
		return nil, err
	}
	statusResponse := myJfrogGetStatusResponse{}
	if err = json.Unmarshal(body, &statusResponse); err != nil {
		return nil, errorutils.CheckError(err)
	}
	ftc.clearHeadlineMsg()
	serverDetails = &config.ServerDetails{
		Url:         statusResponse.PlatformUrl,
		AccessToken: statusResponse.AccessToken,
	}
	ftc.serverDetails = serverDetails
	return serverDetails, nil
}

// Add the given server details to the cli's config by running a 'jf config' command
func configServer(server *config.ServerDetails) error {
	u, err := url.Parse(server.Url)
	if errorutils.CheckError(err) != nil {
		return err
	}
	// Take the server name from host name: https://myjfrog.jfrog.com/ -> myjfrog
	serverId := strings.Split(u.Host, ".")[0]
	configCmd := commands.NewConfigCommand(commands.AddOrEdit, serverId).SetInteractive(false).SetDetails(server)
	if err = configCmd.Run(); err != nil {
		return err
	}
	return commands.NewConfigCommand(commands.Use, serverId).SetInteractive(false).SetDetails(server).Run()
}

type myJfrogGetStatusRequest struct {
	CliRegistrationId string `json:"cliRegistrationId,omitempty"`
}

type myJfrogGetStatusResponse struct {
	CliRegistrationId string `json:"cliRegistrationId,omitempty"`
	Ready             bool   `json:"ready,omitempty"`
	AccessToken       string `json:"accessToken,omitempty"`
	PlatformUrl       string `json:"platformUrl,omitempty"`
}
