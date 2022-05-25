// Copyright 2018 Google Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package main

import (
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/hashicorp/consul/api"
	"log"
	"os"
	"strconv"
	"time"
)
var (
	clusterRegion string
	clusterTagKey string
	clusterTagValue string
	operationsDC string
	retryInterval string
	retryCount string
)


func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

func contains(s []string, str string) bool {
	for _, v := range s {
		if v == str {
			return true
		}
	}
	return false
}

func main() {
	log.Println("Starting consul-wan-autojoin service...")
	clusterRegion = getEnv("OPERATIONS_CONSUL_CLUSTER_REGION", "")
	clusterTagKey = getEnv("OPERATIONS_CONSUL_CLUSTER_TAG_KEY", "")
	clusterTagValue = getEnv("OPERATIONS_CONSUL_CLUSTER_TAG_VALUE", "")
	operationsDC = getEnv("OPERATIONS_CONSUL_DC", "")
	retryInterval = getEnv("AUTO_JOIN_RETRY_INTERVAL", "10")
	retryCount = getEnv("AUTO_JOIN_RETRY_COUNT", "6")

	i, err := strconv.Atoi(retryInterval)
	if err != nil {
		log.Fatalf("AUTO_JOIN_RETRY_INTERVAL is invalid: %s", err)
	}

	retryIntervalDuration := time.Duration(i) * time.Second

	retCount, err := strconv.Atoi(retryCount)
	if err != nil {
		log.Fatalf("AUTO_JOIN_RETRY_COUNT is invalid: %s", err)
	}

	if clusterRegion == "" || clusterTagKey == "" || clusterTagValue == "" {
		log.Println("OPERATIONS_CONSUL_CLUSTER_REGION and/or OPERATIONS_CONSUL_CLUSTER_TAG_KEY and/or OPERATIONS_CONSUL_CLUSTER_TAG_VALUE environment vars were empty or not present, this agent is not configured to autojoin any other cluster")
		os.Exit(0)
	}

	AWSSession, err := session.NewSession(&aws.Config{Region: aws.String(clusterRegion)})
	if err != nil {
		log.Println("Error creating session: ", err)
	}
	svc := ec2.New(AWSSession)
	input := &ec2.DescribeInstancesInput{
		Filters: []*ec2.Filter{
			{
				Name: aws.String(fmt.Sprintf("tag:%s", clusterTagKey)),
				Values: []*string{
					aws.String(clusterTagValue),
				},
			},
		},
	}

	client, err := api.NewClient(api.DefaultConfig())
	if err != nil {
		panic(err)
	}
	// Check if consul agent is alive, wait for a bit if not
	i = 1
	for i < retCount {
		_, err := client.Agent().Self()
		if err != nil {
			if i < retCount {
				log.Printf("Consul agent does not seem healthy. Sleeping %ss...\n", retryInterval)
				i++
				time.Sleep(retryIntervalDuration)
			} else {
				panic(err)
			}
		} else {
			leader, err := client.Status().Leader()
			if err != nil || leader == "" {
				if i < retCount {
					log.Printf("Consul agent does not seem healthy. Sleeping %ss...\n", retryInterval)
					i++
					time.Sleep(retryIntervalDuration)
				} else {
					panic(err)
				}
			} else {
				log.Println("Consul agent is healthy")
				break
			}
		}
	}
	datacenters, err := client.Catalog().Datacenters()
	if err != nil {
		panic(err)
	}
	if contains(datacenters, operationsDC) {
		log.Printf("Cluster is already joined to %s. Nothing to do", operationsDC)
	} else {
		log.Printf("Looking for ec2 instances with tags %s:%s...\n", clusterTagKey, clusterTagValue)
		result, err := svc.DescribeInstances(input)
		if err != nil {
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				default:
					panic(aerr.Error())
				}
			} else {
				panic(err.Error())
			}
		}
		// If alive. Join the cluster instances (if any)
		for _, r := range result.Reservations {
			for _, i := range r.Instances {
				if i != nil && i.PrivateIpAddress != nil {
					fmt.Println(fmt.Sprintf("Found instance with IP: %s. Joining through WAN", *i.PrivateIpAddress))
					err = client.Agent().Join(*i.PrivateIpAddress,true)
					if err != nil {
						panic(err)
					}
				}
			}
		}
	}
}