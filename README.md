# Throttle

[![CI](https://github.com/denpeshkov/throttle/actions/workflows/ci.yml/badge.svg)](https://github.com/denpeshkov/throttle/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/denpeshkov/throttle)](https://goreportcard.com/report/github.com/denpeshkov/throttle)

A Go library that implements a rate limiter using various algorithms, backed by [Redis](https://redis.io).

Currently implemented rate-limiting algorithms:
- Sliding Window Log
- Sliding Window Counter
- Token Bucket

For an overview of the pros and cons of different rate-limiting algorithms, check out this reference: [How to Design a Scalable Rate Limiting Algorithm with Kong API](https://konghq.com/blog/engineering/how-to-design-a-scalable-rate-limiting-algorithm)
