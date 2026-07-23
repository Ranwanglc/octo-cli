package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Mininglamp-OSS/octo-cli/internal/client"
	"github.com/Mininglamp-OSS/octo-cli/internal/cmdutil"
	"github.com/Mininglamp-OSS/octo-cli/internal/output"
)

const (
	htmlResultSchema = "html.publish.result"
	cardProfile      = "octo/v1"
	cardVersion      = "1.5"
)

var (
	slugPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	idPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,255}$`)
)

type publishAndNotifyOptions struct {
	slug        string
	html        string
	data        string
	title       string
	mountType   string
	groupNo     string
	requestID   string
	channelID   string
	channelType int
}

type htmlPublishResult struct {
	Slug       string `json:"slug"`
	Version    int    `json:"version"`
	DocID      string `json:"doc_id"`
	ShareURL   string `json:"share_url"`
	Registered bool   `json:"registered"`
	Status     string `json:"status"`
}

func attachHTMLPublishAndNotify(htmlCmd *cobra.Command, f *cmdutil.Factory) {
	if findChild(htmlCmd, "publish-and-notify") != nil {
		return
	}

	opts := &publishAndNotifyOptions{}
	cmd := &cobra.Command{
		Use:   "publish-and-notify",
		Short: "Publish HTML and send a deterministic result card",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHTMLPublishAndNotify(cmd, f, opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.slug, "slug", "", "stable document slug")
	flags.StringVar(&opts.html, "html", "", "HTML body or @file/@- input")
	flags.StringVar(&opts.data, "data", "", "publish request JSON object or @file/@- input")
	flags.StringVar(&opts.title, "title", "", "document title")
	flags.StringVar(&opts.mountType, "mount-type", "", "registrable mount type: group or space")
	flags.StringVar(&opts.groupNo, "group-no", "", "group number when mount type is group")
	flags.StringVar(&opts.requestID, "request-id", "", "request correlation id")
	flags.StringVar(&opts.channelID, "channel-id", "", "message destination channel id")
	flags.IntVar(&opts.channelType, "channel-type", 0, "message channel type: 1=DM, 2=group, 5=thread")
	cmd.MarkFlagsMutuallyExclusive("html", "data")
	for _, name := range []string{"slug", "title", "mount-type", "request-id", "channel-id", "channel-type"} {
		_ = cmd.MarkFlagRequired(name) //nolint:errcheck // static flag names
	}

	htmlCmd.AddCommand(cmd)
}

func runHTMLPublishAndNotify(cmd *cobra.Command, f *cmdutil.Factory, opts *publishAndNotifyOptions) error {
	if err := validatePublishAndNotifyOptions(f, opts); err != nil {
		return emitCommandError(f, err)
	}

	publishBody, err := buildHTMLPublishBody(f, opts)
	if err != nil {
		return emitCommandError(f, err)
	}

	cli, err := f.Client()
	if err != nil {
		return emitCommandError(f, err)
	}
	publishReq, err := requestForOperation(f, "html.publish", publishBody)
	if err != nil {
		return emitCommandError(f, err)
	}
	publishReq.NoRetry = true
	publishRaw, err := cli.Do(cmd.Context(), publishReq)
	if err != nil {
		return emitCommandError(f, err)
	}

	result, err := parseHTMLPublishResult(publishRaw, opts.slug)
	if err != nil {
		return emitCommandError(f, err)
	}

	payload := buildHTMLResultCard(opts, result)
	sendReq, err := requestForOperation(f, "message.send", map[string]any{
		"channel_id":   opts.channelID,
		"channel_type": opts.channelType,
		"payload":      payload,
	})
	if err != nil {
		return emitCommandError(f, err)
	}
	sendRaw, err := cli.Do(cmd.Context(), sendReq)
	if err != nil {
		return emitCommandError(f, err)
	}

	var message any
	if err := json.Unmarshal(sendRaw, &message); err != nil {
		return emitCommandError(f, output.ErrAPI("INVALID_MESSAGE_RESPONSE", "message send returned invalid JSON", "retry after checking the message service"))
	}
	out, err := json.Marshal(map[string]any{"publish": result, "message": message})
	if err != nil {
		return emitCommandError(f, output.ErrWithHint("internal", "MARSHAL_FAILED", err.Error(), ""))
	}
	return f.EmitSuccess(out)
}

func validatePublishAndNotifyOptions(f *cmdutil.Factory, opts *publishAndNotifyOptions) error {
	if f.Globals != nil && f.Globals.DryRun {
		return output.ErrValidation("--dry-run is not supported by html publish-and-notify", "run html publish separately to inspect it, or execute without --dry-run")
	}
	if !slugPattern.MatchString(opts.slug) {
		return output.ErrValidation("--slug must match ^[A-Za-z0-9_-]{1,64}$", "use a stable ASCII slug without spaces or path separators")
	}
	if strings.TrimSpace(opts.title) == "" {
		return output.ErrValidation("--title must not be empty", "provide the human-readable document title")
	}
	if !idPattern.MatchString(opts.requestID) {
		return output.ErrValidation("--request-id contains invalid characters", "use 1-256 ASCII letters, digits, dot, colon, underscore, or hyphen")
	}
	if !idPattern.MatchString(opts.channelID) {
		return output.ErrValidation("--channel-id contains invalid characters", "use a non-empty platform channel id")
	}
	if opts.channelType != 1 && opts.channelType != 2 && opts.channelType != 5 {
		return output.ErrValidation("--channel-type must be 1, 2, or 5", "use 1=DM, 2=group, or 5=thread")
	}
	switch opts.mountType {
	case "group":
		if !idPattern.MatchString(opts.groupNo) {
			return output.ErrValidation("--group-no is required for mount type group", "pass the destination group number")
		}
	case "space":
	default:
		return output.ErrValidation("--mount-type must be group or space", "thread mounts are not registered and cannot produce a success event")
	}
	if opts.html == "" && opts.data == "" {
		return output.ErrValidation("one of --html or --data is required", "pass HTML directly, via @file/@-, or in a JSON --data object")
	}
	return nil
}

func buildHTMLPublishBody(f *cmdutil.Factory, opts *publishAndNotifyOptions) (map[string]any, error) {
	body := map[string]any{}
	if opts.data != "" {
		raw, err := cmdutil.ParseInput(f, opts.data)
		if err != nil {
			return nil, output.ErrValidation(err.Error(), "check the --data input source")
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, output.ErrValidation(fmt.Sprintf("invalid --data JSON object: %v", err), "pass one JSON object, not an array or scalar")
		}
		if body == nil {
			return nil, output.ErrValidation("--data must be a JSON object", "pass one JSON object containing a non-empty `html` string")
		}
	} else {
		raw, err := cmdutil.ParseInput(f, opts.html)
		if err != nil {
			return nil, output.ErrValidation(err.Error(), "check the --html input source")
		}
		body["html"] = string(raw)
	}

	html, ok := body["html"].(string)
	if !ok || strings.TrimSpace(html) == "" {
		return nil, output.ErrValidation("publish body must contain a non-empty string `html`", "set --html or include `html` in --data")
	}
	body["slug"] = opts.slug
	body["mount_type"] = opts.mountType
	body["meta"] = map[string]any{"title": opts.title}
	delete(body, "group_no")
	delete(body, "thread_id")
	if opts.mountType == "group" {
		body["group_no"] = opts.groupNo
	}
	return body, nil
}

func requestForOperation(f *cmdutil.Factory, operationID string, body any) (*client.Request, error) {
	reg := f.Registry()
	if reg == nil {
		return nil, output.ErrWithHint("internal", "REGISTRY_UNAVAILABLE", "OpenAPI registry is unavailable", "")
	}
	detail, ok := reg.GetOperation(operationID)
	if !ok {
		return nil, output.ErrWithHint("internal", "OPERATION_NOT_FOUND", fmt.Sprintf("operation %q is not registered", operationID), "")
	}
	if strings.Contains(detail.Path, "{") {
		return nil, output.ErrWithHint("internal", "INVALID_OPERATION_PATH", fmt.Sprintf("operation %q requires path parameters", operationID), "")
	}
	return &client.Request{
		Service:             serviceForBaseURL(detail.BaseURLEnv),
		Method:              detail.Method,
		Path:                marketplacePath(detail.Service, detail.Path),
		Body:                body,
		SuppressSpaceHeader: detail.SpaceHeaderSet && !detail.SpaceHeader,
	}, nil
}

func parseHTMLPublishResult(raw []byte, expectedSlug string) (*htmlPublishResult, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, output.ErrAPI("INVALID_PUBLISH_RESPONSE", "HTML publish returned invalid JSON", "do not send a completion message; retry or inspect the HTML service")
	}
	resultRaw := raw
	if data, ok := envelope["data"]; ok {
		resultRaw = data
	}
	var result htmlPublishResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return nil, output.ErrAPI("INVALID_PUBLISH_RESPONSE", "HTML publish result is not an object", "do not send a completion message; inspect the HTML service contract")
	}
	if result.Slug != expectedSlug || !slugPattern.MatchString(result.Slug) {
		return nil, output.ErrAPI("INVALID_PUBLISH_RESPONSE", "HTML publish returned an unexpected slug", "do not send a completion message; verify the publish response")
	}
	if result.Version < 1 {
		return nil, output.ErrAPI("INVALID_PUBLISH_RESPONSE", "HTML publish returned an invalid version", "do not send a completion message; verify the publish response")
	}
	if !idPattern.MatchString(result.DocID) {
		return nil, output.ErrAPI("INVALID_PUBLISH_RESPONSE", "HTML publish returned an invalid doc_id", "do not send a completion message; verify docs-backend registration")
	}
	if err := validateShareURL(result.ShareURL); err != nil {
		return nil, output.ErrAPI("INVALID_PUBLISH_RESPONSE", err.Error(), "do not send a completion message; verify docs-backend registration")
	}
	if !result.Registered {
		return nil, output.ErrAPI("HTML_NOT_REGISTERED", "HTML was published but docs-backend registration did not complete", "do not send a success card; retry registration safely without republishing HTML")
	}
	if result.Status != "published" {
		return nil, output.ErrAPI("INVALID_PUBLISH_STATUS", fmt.Sprintf("HTML publish returned status %q", result.Status), "do not send a success card unless status is published")
	}
	return &result, nil
}

func validateShareURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return fmt.Errorf("HTML publish returned an invalid share_url")
	}
	return nil
}

func buildHTMLResultCard(opts *publishAndNotifyOptions, result *htmlPublishResult) map[string]any {
	return map[string]any{
		"type":         17,
		"profile":      cardProfile,
		"card_version": cardVersion,
		"card": map[string]any{
			"type":    "AdaptiveCard",
			"version": cardVersion,
			"body":    []any{},
			"actions": []any{map[string]any{
				"type":  "Action.OpenUrl",
				"title": "查看详情",
				"url":   result.ShareURL,
			}},
		},
		"octo_result": map[string]any{
			"schema":      htmlResultSchema,
			"version":     1,
			"request_id":  opts.requestID,
			"status":      result.Status,
			"registered":  result.Registered,
			"doc_id":      result.DocID,
			"slug":        result.Slug,
			"doc_version": result.Version,
			"share_url":   result.ShareURL,
		},
	}
}

func emitCommandError(f *cmdutil.Factory, err error) error {
	_ = f.EmitError(err) //nolint:errcheck // preserve the original command error
	return err
}
