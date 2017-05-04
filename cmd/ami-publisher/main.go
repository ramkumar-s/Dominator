package main

import (
	"flag"
	"fmt"
	"github.com/Symantec/Dominator/lib/awsutil"
	"github.com/Symantec/Dominator/lib/constants"
	liblog "github.com/Symantec/Dominator/lib/log"
	"github.com/Symantec/Dominator/lib/log/cmdlogger"
	"github.com/Symantec/Dominator/lib/srpc/setupclient"
	"os"
	"time"
)

var (
	amiName   = flag.String("amiName", "", "AMI Name property")
	expiresIn = flag.Duration("expiresIn", time.Hour,
		"Date to set for the ExpiresAt tag")
	ignoreMissingUnpackers = flag.Bool("ignoreMissingUnpackers", false,
		"If true, do not generate an error for missing unpackers")
	imageServerHostname = flag.String("imageServerHostname", "localhost",
		"Hostname of imageserver")
	imageServerPortNum = flag.Uint("imageServerPortNum",
		constants.ImageServerPortNumber, "Port number of imageserver")
	instanceName = flag.String("instanceName", "ImageUnpacker",
		"The Name tag value for image unpacker instances")
	instanceType = flag.String("instanceType", "t2.medium",
		"Instance type to launch")
	marketplaceImage = flag.String("marketplaceImage",
		"3f8t6t8fp5m9xx18yzwriozxi",
		"Product code (default Debian Jessie amd64)")
	marketplaceLoginName = flag.String("marketplaceLoginName", "admin",
		"Login name for instance booted from marketplace image")
	maxIdleTime = flag.Duration("maxIdleTime", time.Minute*50,
		"Maximum idle time for image unpacker instances")
	minFreeBytes = flag.Uint64("minFreeBytes", 1<<28,
		"minimum number of free bytes in image")
	s3Bucket = flag.String("s3Bucket", "",
		"S3 bucket to upload bundle to (default is EBS-backed AMIs)")
	s3Folder = flag.String("s3Folder", "",
		"S3 folder to upload bundle to (default is EBS-backed AMIs)")
	searchTags              = awsutil.Tags{"Preferred": "true"}
	securityGroupSearchTags awsutil.Tags
	sharingAccountName      = flag.String("sharingAccountName", "",
		"Account from which to share AMIs (for S3-backed)")
	skipTargets awsutil.TargetList
	sshKeyName  = flag.String("sshKeyName", "",
		"Name of SSH key for instance")
	subnetSearchTags awsutil.Tags = awsutil.Tags{"Network": "Private"}
	tags             awsutil.Tags
	targets          awsutil.TargetList
	vpcSearchTags    awsutil.Tags = awsutil.Tags{"Preferred": "true"}
)

func init() {
	flag.Var(&searchTags, "searchTags",
		"Name of tags to use when searching for resources")
	flag.Var(&securityGroupSearchTags, "securityGroupSearchTags",
		"Restrict security group search to given tags")
	flag.Var(&skipTargets, "skipTargets",
		"List of targets to skip (default none). No wildcards permitted")
	flag.Var(&subnetSearchTags, "subnetSearchTags",
		"Restrict subnet search to given tags")
	flag.Var(&tags, "tags", "Tags to apply")
	flag.Var(&targets, "targets",
		"List of targets (default all accounts and regions)")
	flag.Var(&vpcSearchTags, "vpcSearchTags",
		"Restrict VPC search to given tags")
}

func printUsage() {
	fmt.Fprintln(os.Stderr,
		"Usage: ami-publisher [flags...] publish [args...]")
	fmt.Fprintln(os.Stderr, "Common flags:")
	flag.PrintDefaults()
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  add-volumes sizeInGiB")
	fmt.Fprintln(os.Stderr, "  copy-bootstrap-image stream-name")
	fmt.Fprintln(os.Stderr, "  delete results-file...")
	fmt.Fprintln(os.Stderr, "  delete-tags tag-key results-file...")
	fmt.Fprintln(os.Stderr, "  delete-tag-on-unpackers tag-key")
	fmt.Fprintln(os.Stderr, "  expire")
	fmt.Fprintln(os.Stderr, "  import-key-pair name pub-key-file")
	fmt.Fprintln(os.Stderr, "  launch-instances boot-image")
	fmt.Fprintln(os.Stderr, "  launch-instances-for-images results-file...")
	fmt.Fprintln(os.Stderr, "  list-unpackers")
	fmt.Fprintln(os.Stderr, "  prepare-unpackers [stream-name]")
	fmt.Fprintln(os.Stderr, "  publish stream-name image-leaf-name")
	fmt.Fprintln(os.Stderr, "  remove-unused-volumes")
	fmt.Fprintln(os.Stderr, "  set-exclusive-tags key value results-file...")
	fmt.Fprintln(os.Stderr, "  set-tags-on-unpackers")
	fmt.Fprintln(os.Stderr, "  start-instances")
	fmt.Fprintln(os.Stderr, "  stop-idle-unpackers")
	fmt.Fprintln(os.Stderr, "  terminate-instances")
}

type commandFunc func([]string, liblog.Logger)

type subcommand struct {
	command string
	minArgs int
	maxArgs int
	cmdFunc commandFunc
}

var subcommands = []subcommand{
	{"add-volumes", 1, 1, addVolumesSubcommand},
	{"copy-bootstrap-image", 1, 1, copyBootstrapImageSubcommand},
	{"delete", 1, -1, deleteSubcommand},
	{"delete-tags", 2, -1, deleteTagsSubcommand},
	{"delete-tags-on-unpackers", 1, 1, deleteTagsOnUnpackersSubcommand},
	{"expire", 0, 0, expireSubcommand},
	{"import-key-pair", 2, 2, importKeyPairSubcommand},
	{"launch-instances", 1, 1, launchInstancesSubcommand},
	{"launch-instances-for-images", 0, -1, launchInstancesForImagesSubcommand},
	{"list-unpackers", 0, 0, listUnpackersSubcommand},
	{"prepare-unpackers", 0, 1, prepareUnpackersSubcommand},
	{"publish", 2, 2, publishSubcommand},
	{"remove-unused-volumes", 0, 0, removeUnusedVolumesSubcommand},
	{"set-exclusive-tags", 2, -1, setExclusiveTagsSubcommand},
	{"set-tags-on-unpackers", 0, 0, setTagsSubcommand},
	{"start-instances", 0, 0, startInstancesSubcommand},
	{"stop-idle-unpackers", 0, 0, stopIdleUnpackersSubcommand},
	{"terminate-instances", 0, 0, terminateInstancesSubcommand},
}

func main() {
	cmdlogger.SetDatestampsDefault(true)
	flag.Usage = printUsage
	flag.Parse()
	if flag.NArg() < 1 {
		printUsage()
		os.Exit(2)
	}
	logger := cmdlogger.New()
	if err := setupclient.SetupTls(true); err != nil {
		logger.Println(err)
		os.Exit(1)
	}
	numSubcommandArgs := flag.NArg() - 1
	for _, subcommand := range subcommands {
		if flag.Arg(0) == subcommand.command {
			if numSubcommandArgs < subcommand.minArgs ||
				(subcommand.maxArgs >= 0 &&
					numSubcommandArgs > subcommand.maxArgs) {
				printUsage()
				os.Exit(2)
			}
			subcommand.cmdFunc(flag.Args()[1:], logger)
			os.Exit(3)
		}
	}
	printUsage()
	os.Exit(2)
}
