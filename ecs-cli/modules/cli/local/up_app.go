// Copyright 2015-2019 Amazon.com, Inc. or its affiliates. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"). You may
// not use this file except in compliance with the License. A copy of the
// License is located at
//
//	http://aws.amazon.com/apache2.0/
//
// or in the "license" file accompanying this file. This file is distributed
// on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either
// express or implied. See the License for the specific language governing
// permissions and limitations under the License.

package local

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/cli/local/converter"
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/cli/local/docker"
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/cli/local/localproject"
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/cli/local/network"
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/cli/local/secrets"
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/cli/local/secrets/clients"
	"github.com/aws/amazon-ecs-cli/ecs-cli/modules/commands/flags"
	"github.com/aws/aws-sdk-go/service/secretsmanager"
	"github.com/aws/aws-sdk-go/service/ssm"
	composeV3 "github.com/docker/cli/cli/compose/types"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/urfave/cli"
)

// Up creates a Compose file from an ECS task definition and runs it locally.
//
// The Amazon ECS Local Endpoints container needs to be running already for any local ECS task to work
// (see https://github.com/awslabs/amazon-ecs-local-container-endpoints).
// If the container is not running, this command creates a new network for all local ECS tasks to join
// and communicate with the Amazon ECS Local Endpoints container.
func Up(c *cli.Context) {
	// TODO When we don't provide any flags, Create() should just check if a "./docker-compose.local.yml" already exists.
	// If so then Create() should do nothing else, otherwise it should error.
	Create(c)

	network.Setup(docker.NewClient())

	config := readComposeFile(c)
	secrets := readSecrets(config)
	envVars := decryptSecrets(secrets)
	upComposeFile(config, envVars)
}

func readComposeFile(c *cli.Context) *composeV3.Config {
	filename := localproject.LocalOutDefaultFileName
	if c.String(flags.LocalOutputFlag) != "" {
		filename = c.String(flags.LocalOutputFlag)
	}
	config, err := converter.UnmarshalComposeFile(filename)
	if err != nil {
		logrus.Fatalf("Failed to unmarshal Compose file %s due to \n%v", filename, err)
	}
	return config
}

func readSecrets(config *composeV3.Config) []*secrets.ContainerSecret {
	var containerSecrets []*secrets.ContainerSecret
	for _, service := range config.Services {
		for label, secretARN := range service.Labels {
			if !strings.HasPrefix(label, converter.SecretLabelPrefix) {
				continue
			}
			namespaces := strings.Split(label, ".")
			secretName := namespaces[len(namespaces)-1]

			containerSecrets = append(containerSecrets, secrets.NewContainerSecret(service.Name, secretName, secretARN))
		}
	}
	return containerSecrets
}

func decryptSecrets(containerSecrets []*secrets.ContainerSecret) (envVars map[string]string) {
	ssmClient, err := clients.NewSSMDecrypter()
	secretsManagerClient, err := clients.NewSecretsManagerDecrypter()
	if err != nil {
		logrus.Fatalf("Failed to create clients to decrypt secrets due to \n%v", err)
	}

	envVars = make(map[string]string)
	for _, containerSecret := range containerSecrets {
		service, err := containerSecret.ServiceName()
		if err != nil {
			logrus.Fatalf("Failed to retrieve the service of the secret due to \n%v", err)
		}

		decrypted := ""
		err = nil
		switch service {
		case secretsmanager.ServiceName:
			decrypted, err = containerSecret.Decrypt(secretsManagerClient)
		case ssm.ServiceName:
			decrypted, err = containerSecret.Decrypt(ssmClient)
		default:
			err = errors.New(fmt.Sprintf("can't decrypt secret from service %s", service))
		}
		if err != nil {
			logrus.Fatalf("Failed to decrypt secret due to \n%v", err)
		}
		envVars[containerSecret.Name()] = decrypted
	}
	return
}

// upComposeFile starts the containers in the Compose config with the environment variables defined in envVars.
func upComposeFile(config *composeV3.Config, envVars map[string]string) {
	var envs []string
	for env, val := range envVars {
		envs = append(envs, fmt.Sprintf("%s=%s", env, val))
	}

	cmd := exec.Command("docker-compose", "-f", config.Filename, "up", "-d")
	cmd.Env = envs

	out, err := cmd.CombinedOutput()
	if err != nil {
		logrus.Fatalf("Failed to run docker-compose up due to \n%v: %s", err, string(out))
	}
	fmt.Printf("Compose out: %s\n", string(out))
}