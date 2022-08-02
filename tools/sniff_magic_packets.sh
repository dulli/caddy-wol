#!/usr/bin/env bash

tcpdump -i eth0 '(udp and port 7) or (udp and port 9)'