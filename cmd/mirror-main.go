/*
 * MinIO Client, (C) 2015-2019 MinIO, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package cmd

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/minio/cli"
	json "github.com/minio/mc/pkg/colorjson"
	"github.com/minio/mc/pkg/probe"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/encrypt"
	"github.com/minio/minio-go/v7/pkg/notification"
	"github.com/minio/minio/pkg/console"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// mirror specific flags.
var (
	mirrorFlags = []cli.Flag{
		cli.BoolFlag{
			Name:   "force",
			Usage:  "force allows forced overwrite or removal of object(s) on target",
			Hidden: true, // Hidden since this option is deprecated.
		},
		cli.BoolFlag{
			Name:  "overwrite",
			Usage: "overwrite object(s) on target if it differs from source",
		},
		cli.BoolFlag{
			Name:  "fake",
			Usage: "perform a fake mirror operation",
		},
		cli.BoolFlag{
			Name:  "watch, w",
			Usage: "watch and synchronize changes",
		},
		cli.BoolFlag{
			Name:  "remove",
			Usage: "remove extraneous object(s) on target",
		},
		cli.StringFlag{
			Name:  "region",
			Usage: "specify region when creating new bucket(s) on target",
			Value: "us-east-1",
		},
		cli.BoolFlag{
			Name:  "preserve, a",
			Usage: "preserve file(s)/object(s) attributes and bucket(s) policy/locking configuration(s) on target bucket(s)",
		},
		cli.BoolFlag{
			Name:  "md5",
			Usage: "force all upload(s) to calculate md5sum checksum",
		},
		cli.BoolFlag{
			Name:   "multi-master",
			Usage:  "enable multi-master multi-site setup",
			Hidden: true,
		},
		cli.BoolFlag{
			Name:  "active-active",
			Usage: "enable active-active multi-site setup",
		},
		cli.BoolFlag{
			Name:  "disable-multipart",
			Usage: "disable multipart upload feature",
		},
		cli.StringSliceFlag{
			Name:  "exclude",
			Usage: "exclude object(s) that match specified object name pattern",
		},
		cli.StringFlag{
			Name:  "older-than",
			Usage: "filter object(s) older than L days, M hours and N minutes",
		},
		cli.StringFlag{
			Name:  "newer-than",
			Usage: "filter object(s) newer than L days, M hours and N minutes",
		},
		cli.StringFlag{
			Name:  "storage-class, sc",
			Usage: "specify storage class for new object(s) on target",
		},
		cli.StringFlag{
			Name:  "encrypt",
			Usage: "encrypt/decrypt objects (using server-side encryption with server managed keys)",
		},
		cli.StringFlag{
			Name:  "attr",
			Usage: "add custom metadata for all objects",
		},
		cli.StringFlag{
			Name:  "monitoring-port",
			Usage: "if specified, a new prometheus endpoint will be created to report mirroring activity",
		},
	}
)

//  Mirror folders recursively from a single source to many destinations
var mirrorCmd = cli.Command{
	Name:         "mirror",
	Usage:        "synchronize object(s) to a remote site",
	Action:       mainMirror,
	OnUsageError: onUsageError,
	Before:       setGlobalsFromContext,
	Flags:        append(append(mirrorFlags, ioFlags...), globalFlags...),
	CustomHelpTemplate: `NAME:
  {{.HelpName}} - {{.Usage}}

USAGE:
  {{.HelpName}} [FLAGS] SOURCE TARGET

FLAGS:
  {{range .VisibleFlags}}{{.}}
  {{end}}
ENVIRONMENT VARIABLES:
   MC_ENCRYPT:      list of comma delimited prefixes
   MC_ENCRYPT_KEY:  list of comma delimited prefix=secret values

EXAMPLES:
  01. Mirror a bucket recursively from MinIO cloud storage to a bucket on Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} play/photos/2014 s3/backup-photos

  02. Mirror a local folder recursively to Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} backup/ s3/archive

  03. Only mirror files that are newer than 7 days, 10 hours and 30 minutes to Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} --newer-than "7d10h30m" backup/ s3/archive

  04. Mirror a bucket from aliased Amazon S3 cloud storage to a folder on Windows.
      {{.Prompt}} {{.HelpName}} s3\documents\2014\ C:\backup\2014

  05. Mirror a bucket from aliased Amazon S3 cloud storage to a local folder use '--overwrite' to overwrite destination.
      {{.Prompt}} {{.HelpName}} --overwrite s3/miniocloud miniocloud-backup

  06. Mirror a bucket from MinIO cloud storage to a bucket on Amazon S3 cloud storage and remove any extraneous
      files on Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} --remove play/photos/2014 s3/backup-photos/2014

  07. Continuously mirror a local folder recursively to MinIO cloud storage. '--watch' continuously watches for
      new objects, uploads and removes extraneous files on Amazon S3 cloud storage.
      {{.Prompt}} {{.HelpName}} --remove --watch /var/lib/backups play/backups

  08. Continuously mirror all buckets and objects from site 1 to site 2, removed buckets and objects will be reflected as well.
      {{.Prompt}} {{.HelpName}} --remove --watch site1-alias/ site2-alias/

  09. Mirror a bucket from aliased Amazon S3 cloud storage to a local folder.
      Exclude all .* files and *.temp files when mirroring.
      {{.Prompt}} {{.HelpName}} --exclude ".*" --exclude "*.temp" s3/test ~/test

  10. Mirror objects newer than 10 days from bucket test to a local folder.
      {{.Prompt}} {{.HelpName}} --newer-than 10d s3/test ~/localfolder

  11. Mirror objects older than 30 days from Amazon S3 bucket test to a local folder.
      {{.Prompt}} {{.HelpName}} --older-than 30d s3/test ~/test

  12. Mirror server encrypted objects from MinIO cloud storage to a bucket on Amazon S3 cloud storage
      {{.Prompt}} {{.HelpName}} --encrypt-key "minio/photos=32byteslongsecretkeymustbegiven1,s3/archive=32byteslongsecretkeymustbegiven2" minio/photos/ s3/archive/

  13. Mirror server encrypted objects from MinIO cloud storage to a bucket on Amazon S3 cloud storage. In case the encryption key contains
      non-printable character like tab, pass the base64 encoded string as key.
      {{.Prompt}} {{.HelpName}} --encrypt-key "s3/photos/=32byteslongsecretkeymustbegiven1,play/archive/=MzJieXRlc2xvbmdzZWNyZXRrZQltdXN0YmVnaXZlbjE=" s3/photos/ play/archive/

  14. Update 'Cache-Control' header on all existing objects recursively.
      {{.Prompt}} {{.HelpName}} --attr "Cache-Control=max-age=90000,min-fresh=9000" myminio/video-files myminio/video-files

  15. Mirror a local folder recursively to Amazon S3 cloud storage and preserve all local file attributes.
      {{.Prompt}} {{.HelpName}} -a backup/ s3/archive

  16. Cross mirror between sites in a active-active deployment.
      Site-A: {{.Prompt}} {{.HelpName}} --active-active siteA siteB
      Site-B: {{.Prompt}} {{.HelpName}} --active-active siteB siteA
`,
}

var (
	s3mirrorTotalOps = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mc_mirror_total_s3ops",
		Help: "The total number of failed mirror events",
	})
	s3mirrorFailedOps = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mc_mirror_failed_s3ops",
		Help: "The total number of failed mirror events",
	})
	s3mirrorRestarts = promauto.NewCounter(prometheus.CounterOpts{
		Name: "mc_mirror_total_restarts",
		Help: "The number of mirroring restart",
	})
)

const uaMirrorAppName = "mc-mirror"

type mirrorJob struct {
	stopCh chan struct{}

	// mutex for shutdown, this prevents the shutdown
	// to be initiated multiple times
	m sync.Mutex

	// the global watcher object, which receives notifications of created
	// and deleted files
	watcher *Watcher

	// Hold operation status information
	status Status

	parallel *ParallelManager

	// channel for status messages
	statusCh chan URLs

	TotalObjects int64
	TotalBytes   int64

	sourceURL string
	targetURL string

	opts mirrorOptions
}

// mirrorMessage container for file mirror messages
type mirrorMessage struct {
	Status     string `json:"status"`
	Source     string `json:"source"`
	Target     string `json:"target"`
	Size       int64  `json:"size"`
	TotalCount int64  `json:"totalCount"`
	TotalSize  int64  `json:"totalSize"`
}

// String colorized mirror message
func (m mirrorMessage) String() string {
	return console.Colorize("Mirror", fmt.Sprintf("`%s` -> `%s`", m.Source, m.Target))
}

// JSON jsonified mirror message
func (m mirrorMessage) JSON() string {
	m.Status = "success"
	mirrorMessageBytes, e := json.MarshalIndent(m, "", " ")
	fatalIf(probe.NewError(e), "Unable to marshal into JSON.")

	return string(mirrorMessageBytes)
}

func (mj *mirrorJob) doCreateBucket(ctx context.Context, sURLs URLs) URLs {
	if mj.opts.isFake {
		return sURLs.WithError(nil)
	}

	// Construct proper path with alias.
	aliasedURL := filepath.Join(sURLs.TargetAlias, sURLs.TargetContent.URL.Path)
	clnt, pErr := newClient(aliasedURL)
	if pErr != nil {
		return sURLs.WithError(pErr)
	}

	err := clnt.MakeBucket(ctx, "", mj.opts.isOverwrite, false)
	if err != nil {
		return sURLs.WithError(err)
	}

	return sURLs.WithError(nil)
}

func (mj *mirrorJob) doDeleteBucket(ctx context.Context, sURLs URLs) URLs {
	if mj.opts.isFake {
		return sURLs.WithError(nil)
	}

	// Construct proper path with alias.
	aliasedURL := filepath.Join(sURLs.TargetAlias, sURLs.TargetContent.URL.Path)
	clnt, pErr := newClient(aliasedURL)
	if pErr != nil {
		return sURLs.WithError(pErr)
	}

	var contentCh = make(chan *ClientContent, 1)
	contentCh <- &ClientContent{URL: clnt.GetURL()}
	close(contentCh)

	for err := range clnt.Remove(ctx, false, true, false, contentCh) {
		return sURLs.WithError(err)
	}

	return sURLs.WithError(nil)
}

// doRemove - removes files on target.
func (mj *mirrorJob) doRemove(ctx context.Context, sURLs URLs) URLs {
	if mj.opts.isFake {
		return sURLs.WithError(nil)
	}

	// Construct proper path with alias.
	targetWithAlias := filepath.Join(sURLs.TargetAlias, sURLs.TargetContent.URL.Path)
	clnt, pErr := newClient(targetWithAlias)
	if pErr != nil {
		return sURLs.WithError(pErr)
	}
	clnt.AddUserAgent(uaMirrorAppName, ReleaseTag)
	contentCh := make(chan *ClientContent, 1)
	contentCh <- &ClientContent{URL: *newClientURL(sURLs.TargetContent.URL.Path)}
	close(contentCh)
	isRemoveBucket := false
	errorCh := clnt.Remove(ctx, false, isRemoveBucket, false, contentCh)
	for pErr := range errorCh {
		if pErr != nil {
			switch pErr.ToGoError().(type) {
			case PathInsufficientPermission:
				// Ignore Permission error.
				continue
			}
			return sURLs.WithError(pErr)
		}
	}

	return sURLs.WithError(nil)
}

// doMirror - Mirror an object to multiple destination. URLs status contains a copy of sURLs and error if any.
func (mj *mirrorJob) doMirrorWatch(ctx context.Context, targetPath string, tgtSSE encrypt.ServerSide, sURLs URLs) URLs {
	shouldQueue := false
	if !mj.opts.isOverwrite && !mj.opts.activeActive {
		targetClient, err := newClient(targetPath)
		if err != nil {
			// cannot create targetclient
			return sURLs.WithError(err)
		}
		_, err = targetClient.Stat(ctx, StatOptions{sse: tgtSSE})
		if err == nil {
			if !sURLs.SourceContent.RetentionEnabled && !sURLs.SourceContent.LegalHoldEnabled {
				return sURLs.WithError(probe.NewError(ObjectAlreadyExists{}))
			}
		} // doesn't exist
		shouldQueue = true
	}
	if shouldQueue || mj.opts.isOverwrite || mj.opts.activeActive {
		// adjust total, because we want to show progress of
		// the item still queued to be copied.
		mj.status.Add(sURLs.SourceContent.Size)
		mj.status.SetTotal(mj.status.Get()).Update()
		mj.status.AddCounts(1)
		sURLs.TotalSize = mj.status.Get()
		sURLs.TotalCount = mj.status.GetCounts()
		return mj.doMirror(ctx, sURLs)
	}
	return sURLs.WithError(probe.NewError(ObjectAlreadyExists{}))
}

// doMirror - Mirror an object to multiple destination. URLs status contains a copy of sURLs and error if any.
func (mj *mirrorJob) doMirror(ctx context.Context, sURLs URLs) URLs {

	if sURLs.Error != nil { // Erroneous sURLs passed.
		return sURLs.WithError(sURLs.Error.Trace())
	}

	// For a fake mirror make sure we update respective progress bars
	// and accounting readers under relevant conditions.
	if mj.opts.isFake {
		if sURLs.SourceContent != nil {
			mj.status.Add(sURLs.SourceContent.Size)
		}
		mj.status.Update()
		return sURLs.WithError(nil)
	}

	sourceAlias := sURLs.SourceAlias
	sourceURL := sURLs.SourceContent.URL
	targetAlias := sURLs.TargetAlias
	targetURL := sURLs.TargetContent.URL
	length := sURLs.SourceContent.Size

	mj.status.SetCaption(sourceURL.String() + ": ")

	// Initialize target metadata.
	sURLs.TargetContent.Metadata = make(map[string]string)

	if mj.opts.storageClass != "" {
		sURLs.TargetContent.Metadata["X-Amz-Storage-Class"] = mj.opts.storageClass
	}

	if mj.opts.activeActive {
		srcModTime := getSourceModTimeKey(sURLs.SourceContent.Metadata)
		// If the source object already has source modtime attribute set, then
		// use it in target. Otherwise use the S3 modtime instead.
		if srcModTime != "" {
			sURLs.TargetContent.Metadata[activeActiveSourceModTimeKey] = srcModTime
		} else {
			sURLs.TargetContent.Metadata[activeActiveSourceModTimeKey] = sURLs.SourceContent.Time.Format(time.RFC3339Nano)
		}
	}

	// Initialize additional target user metadata.
	sURLs.TargetContent.UserMetadata = mj.opts.userMetadata

	sourcePath := filepath.ToSlash(filepath.Join(sourceAlias, sourceURL.Path))
	targetPath := filepath.ToSlash(filepath.Join(targetAlias, targetURL.Path))
	mj.status.PrintMsg(mirrorMessage{
		Source:     sourcePath,
		Target:     targetPath,
		Size:       length,
		TotalCount: sURLs.TotalCount,
		TotalSize:  sURLs.TotalSize,
	})
	sURLs.MD5 = mj.opts.md5
	sURLs.DisableMultipart = mj.opts.disableMultipart
	return uploadSourceToTargetURL(ctx, sURLs, mj.status, mj.opts.encKeyDB, mj.opts.isMetadata)
}

// Update progress status
func (mj *mirrorJob) monitorMirrorStatus() (errDuringMirror bool) {
	// now we want to start the progress bar
	mj.status.Start()
	defer mj.status.Finish()

	for sURLs := range mj.statusCh {
		// Update prometheus fields
		s3mirrorTotalOps.Inc()

		if sURLs.Error != nil {
			s3mirrorFailedOps.Inc()
			switch {
			case sURLs.SourceContent != nil:
				if !isErrIgnored(sURLs.Error) {
					errorIf(sURLs.Error.Trace(sURLs.SourceContent.URL.String()),
						fmt.Sprintf("Failed to copy `%s`.", sURLs.SourceContent.URL.String()))
					errDuringMirror = true
				}
			case sURLs.TargetContent != nil:
				// When sURLs.SourceContent is nil, we know that we have an error related to removing
				errorIf(sURLs.Error.Trace(sURLs.TargetContent.URL.String()),
					fmt.Sprintf("Failed to remove `%s`.", sURLs.TargetContent.URL.String()))
				errDuringMirror = true
			default:
				if sURLs.ErrorCond == differInUnknown {
					errorIf(sURLs.Error.Trace(), "Failed to perform mirroring")
				} else {
					errorIf(sURLs.Error.Trace(),
						"Failed to perform mirroring, with error condition (%s)", sURLs.ErrorCond)
				}
				errDuringMirror = true
			}
			if mj.opts.activeActive {
				close(mj.stopCh)
				break
			}
		}

		if sURLs.SourceContent != nil {
		} else if sURLs.TargetContent != nil {
			// Construct user facing message and path.
			targetPath := filepath.ToSlash(filepath.Join(sURLs.TargetAlias, sURLs.TargetContent.URL.Path))
			size := sURLs.TargetContent.Size
			mj.status.PrintMsg(rmMessage{Key: targetPath, Size: size})
		}
	}

	return
}

func (mj *mirrorJob) watchMirrorEvents(ctx context.Context, events []EventInfo) {
	for _, event := range events {
		// It will change the expanded alias back to the alias
		// again, by replacing the sourceUrlFull with the sourceAlias.
		// This url will be used to mirror.
		sourceAlias, sourceURLFull, _ := mustExpandAlias(mj.sourceURL)

		// If the passed source URL points to fs, fetch the absolute src path
		// to correctly calculate targetPath
		if sourceAlias == "" {
			tmpSrcURL, err := filepath.Abs(sourceURLFull)
			if err == nil {
				sourceURLFull = tmpSrcURL
			}
		}
		eventPath := event.Path
		if runtime.GOOS == "darwin" {
			// Strip the prefixes in the event path. Happens in darwin OS only
			eventPath = eventPath[strings.Index(eventPath, sourceURLFull):]
		} else if runtime.GOOS == "windows" {
			// Shared folder as source URL and if event path is an absolute path.
			eventPath = getEventPathURLWin(mj.sourceURL, eventPath)
		}

		sourceURL := newClientURL(eventPath)

		// build target path, it is the relative of the eventPath with the sourceUrl
		// joined to the targetURL.
		sourceSuffix := strings.TrimPrefix(eventPath, sourceURLFull)
		//ymfix Skip the object,event http://10.0.0.1:9000/test2/1.png sourceURLFull http://10.0.0.1:9000/test1
		if sourceSuffix == eventPath {
			continue
		}
		//Skip the object, if it matches the Exclude options provided
		if matchExcludeOptions(mj.opts.excludeOptions, sourceSuffix) {
			continue
		}

		targetPath := urlJoinPath(mj.targetURL, sourceSuffix)

		// newClient needs the unexpanded  path, newCLientURL needs the expanded path
		targetAlias, expandedTargetPath, _ := mustExpandAlias(targetPath)
		targetURL := newClientURL(expandedTargetPath)
		tgtSSE := getSSE(targetPath, mj.opts.encKeyDB[targetAlias])

		if strings.HasPrefix(string(event.Type), "s3:ObjectCreated:") {
			sourceModTime, _ := time.Parse(time.RFC3339Nano, event.Time)
			mirrorURL := URLs{
				SourceAlias: sourceAlias,
				SourceContent: &ClientContent{
					URL:              *sourceURL,
					RetentionEnabled: event.Type == notification.EventType("s3:ObjectCreated:PutRetention"),
					LegalHoldEnabled: event.Type == notification.EventType("s3:ObjectCreated:PutLegalHold"),
					Size:             event.Size,
					Time:             sourceModTime,
					Metadata:         event.UserMetadata,
				},
				TargetAlias:      targetAlias,
				TargetContent:    &ClientContent{URL: *targetURL},
				MD5:              mj.opts.md5,
				DisableMultipart: mj.opts.disableMultipart,
				encKeyDB:         mj.opts.encKeyDB,
			}
			if mj.opts.activeActive &&
				(getSourceModTimeKey(mirrorURL.SourceContent.Metadata) != "" ||
					getSourceModTimeKey(mirrorURL.SourceContent.UserMetadata) != "") {
				// If source has active-active attributes, it means that the
				// object was uploaded by "mc mirror", hence ignore the event
				// to avoid copying it.
				continue
			}
			mj.parallel.queueTask(func() URLs {
				return mj.doMirrorWatch(ctx, targetPath, tgtSSE, mirrorURL)
			})
		} else if event.Type == notification.ObjectRemovedDelete {
			if strings.Contains(event.UserAgent, uaMirrorAppName) {
				continue
			}
			mirrorURL := URLs{
				SourceAlias:      sourceAlias,
				SourceContent:    nil,
				TargetAlias:      targetAlias,
				TargetContent:    &ClientContent{URL: *targetURL},
				MD5:              mj.opts.md5,
				DisableMultipart: mj.opts.disableMultipart,
				encKeyDB:         mj.opts.encKeyDB,
			}
			mirrorURL.TotalCount = mj.status.GetCounts()
			mirrorURL.TotalSize = mj.status.Get()
			if mirrorURL.TargetContent != nil && (mj.opts.isRemove || mj.opts.activeActive) {
				mj.parallel.queueTask(func() URLs {
					return mj.doRemove(ctx, mirrorURL)
				})
			}
		} else if event.Type == notification.BucketCreatedAll {
			mirrorURL := URLs{
				SourceAlias:   sourceAlias,
				SourceContent: &ClientContent{URL: *sourceURL},
				TargetAlias:   targetAlias,
				TargetContent: &ClientContent{URL: *targetURL},
			}
			mj.parallel.queueTaskWithBarrier(func() URLs {
				return mj.doCreateBucket(ctx, mirrorURL)
			})
		} else if event.Type == notification.BucketRemovedAll && mj.opts.isRemove {
			mirrorURL := URLs{
				TargetAlias:   targetAlias,
				TargetContent: &ClientContent{URL: *targetURL},
			}
			mj.parallel.queueTaskWithBarrier(func() URLs {
				return mj.doDeleteBucket(ctx, mirrorURL)
			})
		}

	}
}

// this goroutine will watch for notifications, and add modified objects to the queue
func (mj *mirrorJob) watchMirror(ctx context.Context, stopParallel func()) {
	for {
		select {
		case events, ok := <-mj.watcher.Events():
			if !ok {
				stopParallel()
				return
			}
			mj.watchMirrorEvents(ctx, events)
		case err, ok := <-mj.watcher.Errors():
			if !ok {
				stopParallel()
				return
			}
			switch err.ToGoError().(type) {
			case APINotImplemented:
				errorIf(err.Trace(),
					"Unable to Watch on source, perhaps source doesn't support Watching for events")
				return
			}
			if err != nil {
				mj.parallel.queueTask(func() URLs {
					return URLs{Error: err}
				})
			}
		case <-globalContext.Done():
			stopParallel()
			return
		}
	}
}

func (mj *mirrorJob) watchURL(ctx context.Context, sourceClient Client) *probe.Error {
	return mj.watcher.Join(ctx, sourceClient, true)
}

// Fetch urls that need to be mirrored
func (mj *mirrorJob) startMirror(ctx context.Context, cancelMirror context.CancelFunc, stopParallel func()) {
	// Do not run multiple startMirror's
	mj.m.Lock()
	defer mj.m.Unlock()

	URLsCh := prepareMirrorURLs(ctx, mj.sourceURL, mj.targetURL, mj.opts)

	for {
		select {
		case sURLs, ok := <-URLsCh:
			if !ok {
				stopParallel()
				return
			}
			if sURLs.Error != nil {
				mj.statusCh <- sURLs
				continue
			}

			if sURLs.SourceContent != nil {
				if isOlder(sURLs.SourceContent.Time, mj.opts.olderThan) {
					continue
				}
				if isNewer(sURLs.SourceContent.Time, mj.opts.newerThan) {
					continue
				}
			}

			if sURLs.SourceContent != nil {
				mj.status.Add(sURLs.SourceContent.Size)
			}

			mj.status.SetTotal(mj.status.Get()).Update()
			mj.status.AddCounts(1)

			// Save total count.
			sURLs.TotalCount = mj.status.GetCounts()
			// Save totalSize.
			sURLs.TotalSize = mj.status.Get()

			if sURLs.SourceContent != nil {
				mj.parallel.queueTask(func() URLs {
					return mj.doMirror(ctx, sURLs)
				})
			} else if sURLs.TargetContent != nil && mj.opts.isRemove {
				mj.parallel.queueTask(func() URLs {
					return mj.doRemove(ctx, sURLs)
				})
			}
		case <-globalContext.Done():
			stopParallel()
			return
		case <-mj.stopCh:
			stopParallel()
			return
		}
	}
}

// when using a struct for copying, we could save a lot of passing of variables
func (mj *mirrorJob) mirror(ctx context.Context, cancelMirror context.CancelFunc) bool {

	var wg sync.WaitGroup

	// Starts watcher loop for watching for new events.
	if mj.opts.isWatch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stopParallel := func() {
				mj.parallel.stopAndWait()
				cancelMirror()
			}
			mj.watchMirror(ctx, stopParallel)
		}()
	}

	// Start mirroring.
	wg.Add(1)
	go func() {
		defer wg.Done()
		stopParallel := func() {
			if !mj.opts.isWatch {
				mj.parallel.stopAndWait()
				cancelMirror()
			}
		}
		// startMirror locks and blocks itself.
		mj.startMirror(ctx, cancelMirror, stopParallel)
	}()

	// Close statusCh when both watch & mirror quits
	go func() {
		wg.Wait()
		close(mj.statusCh)
	}()

	return mj.monitorMirrorStatus()
}

func newMirrorJob(srcURL, dstURL string, opts mirrorOptions) *mirrorJob {
	mj := mirrorJob{
		stopCh: make(chan struct{}),

		sourceURL: srcURL,
		targetURL: dstURL,
		opts:      opts,
		statusCh:  make(chan URLs),
		watcher:   NewWatcher(UTCNow()),
	}

	mj.parallel = newParallelManager(mj.statusCh)

	// we'll define the status to use here,
	// do we want the quiet status? or the progressbar
	if globalQuiet {
		mj.status = NewQuietStatus(mj.parallel)
	} else if globalJSON {
		mj.status = NewQuietStatus(mj.parallel)
	} else {
		mj.status = NewProgressStatus(mj.parallel)
	}

	return &mj
}

// copyBucketPolicies - copy policies from source to dest
func copyBucketPolicies(ctx context.Context, srcClt, dstClt Client, isOverwrite bool) *probe.Error {
	rules, err := srcClt.GetAccessRules(ctx)
	if err != nil {
		switch err.ToGoError().(type) {
		case APINotImplemented:
			return nil
		}
		return err
	}
	// Set found rules to target bucket if permitted
	for _, r := range rules {
		originalRule, _, err := dstClt.GetAccess(ctx)
		if err != nil {
			return err
		}
		// Set rule only if it doesn't exist in the target bucket
		// or force flag is activated
		if originalRule == "none" || isOverwrite {
			err = dstClt.SetAccess(ctx, r, false)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func getEventPathURLWin(srcURL, eventPath string) string {
	// A rename or move or sometimes even write event sets eventPath as an absolute filepath.
	// If the watch folder is a shared folder the write events show the entire event path,
	// from which we need to deduce the correct path relative to the source URL
	var eventRelPath, lastPathPrefix string
	var lastPathPrefixPos int
	sourceURLpathList := strings.Split(srcURL, slashSeperator)
	lenSrcURLSlice := len(sourceURLpathList)
	shdModifyEventPath := filepath.IsAbs(eventPath) && !filepath.IsAbs(srcURL) && lenSrcURLSlice > 1

	if shdModifyEventPath {
		lastPathPrefix = sourceURLpathList[lenSrcURLSlice-1]
		lastPathPrefixPos = strings.Index(eventPath, lastPathPrefix)
	}
	canModifyEventPath := shdModifyEventPath && lastPathPrefix != "" && lastPathPrefixPos > 0
	canModifyEventPath = canModifyEventPath && lastPathPrefixPos+len(lastPathPrefix) < len(eventPath)
	if canModifyEventPath {
		eventRelPath = filepath.ToSlash(eventPath[lastPathPrefixPos+len(lastPathPrefix):])
		eventPath = srcURL + eventRelPath
	}
	return eventPath
}

// runMirror - mirrors all buckets to another S3 server
func runMirror(ctx context.Context, cancelMirror context.CancelFunc, srcURL, dstURL string, cli *cli.Context, encKeyDB map[string][]prefixSSEPair) bool {
	// Parse metadata.
	userMetadata := make(map[string]string)
	if cli.String("attr") != "" {
		var err *probe.Error
		userMetadata, err = getMetaDataEntry(cli.String("attr"))
		fatalIf(err, "Unable to parse attribute %v", cli.String("attr"))
	}

	srcClt, err := newClient(srcURL)
	fatalIf(err, "Unable to initialize `"+srcURL+"`.")

	dstClt, err := newClient(dstURL)
	fatalIf(err, "Unable to initialize `"+dstURL+"`.")

	// This is kept for backward compatibility, `--force` means --overwrite.
	isOverwrite := cli.Bool("force")
	if !isOverwrite {
		isOverwrite = cli.Bool("overwrite")
	}

	isWatch := cli.Bool("watch") || cli.Bool("multi-master") || cli.Bool("active-active")
	isRemove := cli.Bool("remove")

	// preserve is also expected to be overwritten if necessary
	isMetadata := cli.Bool("a") || isWatch || len(userMetadata) > 0
	isOverwrite = isOverwrite || isMetadata

	mopts := mirrorOptions{
		isFake:           cli.Bool("fake"),
		isRemove:         isRemove,
		isOverwrite:      isOverwrite,
		isWatch:          isWatch,
		isMetadata:       isMetadata,
		md5:              cli.Bool("md5"),
		disableMultipart: cli.Bool("disable-multipart"),
		excludeOptions:   cli.StringSlice("exclude"),
		olderThan:        cli.String("older-than"),
		newerThan:        cli.String("newer-than"),
		storageClass:     cli.String("storage-class"),
		userMetadata:     userMetadata,
		encKeyDB:         encKeyDB,
		activeActive:     isWatch,
	}

	// Create a new mirror job and execute it
	mj := newMirrorJob(srcURL, dstURL, mopts)

	preserve := cli.Bool("preserve")

	createDstBuckets := dstClt.GetURL().Type == objectStorage && dstClt.GetURL().Path == string(dstClt.GetURL().Separator)
	mirrorSrcBuckets := srcClt.GetURL().Type == objectStorage && srcClt.GetURL().Path == string(srcClt.GetURL().Separator)
	mirrorBucketsToBuckets := mirrorSrcBuckets && createDstBuckets

	if mirrorSrcBuckets || createDstBuckets {
		// Synchronize buckets using dirDifference function
		for d := range dirDifference(ctx, srcClt, dstClt, srcURL, dstURL) {
			if d.Error != nil {
				if mj.opts.activeActive {
					errorIf(d.Error, "Failed to start mirroring.. retrying")
					return true
				}
				mj.status.fatalIf(d.Error, "Failed to start mirroring.")
			}

			if d.Diff == differInSecond {
				diffBucket := strings.TrimPrefix(d.SecondURL, dstClt.GetURL().String())
				if isRemove {
					aliasedDstBucket := path.Join(dstURL, diffBucket)
					err := deleteBucket(ctx, aliasedDstBucket)
					mj.status.fatalIf(err, "Failed to start mirroring.")
				}
				continue
			}

			sourceSuffix := strings.TrimPrefix(d.FirstURL, srcClt.GetURL().String())

			newSrcURL := path.Join(srcURL, sourceSuffix)
			newTgtURL := path.Join(dstURL, sourceSuffix)

			newSrcClt, _ := newClient(newSrcURL)
			newDstClt, _ := newClient(newTgtURL)

			if d.Diff == differInFirst {
				var (
					withLock bool
					mode     minio.RetentionMode
					validity uint64
					unit     minio.ValidityUnit
					err      *probe.Error
				)
				if preserve && mirrorBucketsToBuckets {
					_, mode, validity, unit, err = newSrcClt.GetObjectLockConfig(ctx)
					if err == nil {
						withLock = true
					}
				}
				// Bucket only exists in the source, create the same bucket in the destination
				if err := newDstClt.MakeBucket(ctx, cli.String("region"), false, withLock); err != nil {
					errorIf(err, "Unable to create bucket at `"+newTgtURL+"`.")
					continue
				}
				if preserve && mirrorBucketsToBuckets {
					// object lock configuration set on bucket
					if mode != "" {
						err = newDstClt.SetObjectLockConfig(ctx, mode, validity, unit)
						errorIf(err, "Unable to set object lock config in `"+newTgtURL+"`.")
						if err != nil && mj.opts.activeActive {
							return true
						}
						if err == nil {
							mj.opts.md5 = true
						}
					}
					errorIf(copyBucketPolicies(ctx, newSrcClt, newDstClt, isOverwrite),
						"Unable to copy bucket policies to `"+newDstClt.GetURL().String()+"`.")
				}
			}
		}
	}

	if mj.opts.isWatch {
		// monitor mode will watch the source folders for changes,
		// and queue them for copying.
		if err := mj.watchURL(ctx, srcClt); err != nil {
			if mj.opts.activeActive {
				errorIf(err, "Failed to start monitoring.. retrying")
				return true
			}
			mj.status.fatalIf(err, "Failed to start monitoring.")
		}
	}

	return mj.mirror(ctx, cancelMirror)
}

// Main entry point for mirror command.
func mainMirror(cliCtx *cli.Context) error {
	// Additional command specific theme customization.
	console.SetColor("Mirror", color.New(color.FgGreen, color.Bold))

	ctx, cancelMirror := context.WithCancel(globalContext)
	defer cancelMirror()

	// Parse encryption keys per command.
	encKeyDB, err := getEncKeys(cliCtx)
	fatalIf(err, "Unable to parse encryption keys.")

	// check 'mirror' cli arguments.
	srcURL, tgtURL := checkMirrorSyntax(ctx, cliCtx, encKeyDB)

	if prometheusPort := cliCtx.String("monitoring-port"); prometheusPort != "" {
		http.Handle("/metrics", promhttp.Handler())
		go func() {
			if e := http.ListenAndServe(prometheusPort, nil); e != nil {
				fatalIf(probe.NewError(e), "Unable to setup monitoring endpoint.")
			}

		}()
	}

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	for {
		select {
		case <-ctx.Done():
			return exitStatus(globalErrorExitStatus)
		default:
			errorDetected := runMirror(ctx, cancelMirror, srcURL, tgtURL, cliCtx, encKeyDB)
			if cliCtx.Bool("watch") || cliCtx.Bool("multi-master") || cliCtx.Bool("active-active") {
				s3mirrorRestarts.Inc()
				time.Sleep(time.Duration(r.Float64() * float64(2*time.Second)))
				continue
			}
			if errorDetected {
				return exitStatus(globalErrorExitStatus)
			}
			return nil
		}
	}
}
