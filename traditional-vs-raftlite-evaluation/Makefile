SHELL := /bin/bash
ROOT := $(dir $(abspath $(lastword $(MAKEFILE_LIST))))

.PHONY: build datasets run-all clean-results

build:
	cd $(ROOT) && go build -o bin/ ./cmd/...
	cd $(ROOT)/.. && go build -o $(ROOT)/bin/raftlite ./cmd/raftlite

datasets: build
	cd $(ROOT) && go run ./cmd/gen

run-all: build
	cd $(ROOT) && bash scripts/run-all.sh

clean-results:
	rm -f $(ROOT)/results/raw/*.csv $(ROOT)/results/raw/*-summary.csv
	rm -f $(ROOT)/results/summary.csv $(ROOT)/results/all-runs.json
	rm -rf $(ROOT)/results/graphs
