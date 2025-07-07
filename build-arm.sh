#!/bin/bash

# Stop on any error
set -e

# Check parameters
if [ "$#" -ne 1 ]; then
    echo "Usage: $0 'v0.13.4-envoy1.33.2,v0.13.4-envoy1.32.3'"
    exit 1
fi

# Check required environment variables
if [ -z "$GITHUB_TOKEN" ]; then
    echo "⚠️  GITHUB_TOKEN environment variable is required for private repository access"
    exit 1
fi

if [ -z "$DOCKER_USERNAME" ] || [ -z "$DOCKER_PASSWORD" ]; then
    echo "⚠️  DOCKER_USERNAME and DOCKER_PASSWORD environment variables are required"
    exit 1
fi

# Docker login check and config setup
echo "🔑 Setting up Docker authentication..."
# Create temporary docker config
DOCKER_CONFIG_DIR=$(mktemp -d)
DOCKER_BUILDX_DIR=$(mktemp -d)
echo "{\"auths\": {\"https://index.docker.io/v1/\": {\"auth\": \"$(echo -n "${DOCKER_USERNAME}:${DOCKER_PASSWORD}" | base64)\"}}}" > "${DOCKER_CONFIG_DIR}/config.json"
echo "✅ Docker config created"

# Create a temporary directory for the build
TEMP_DIR=$(mktemp -d)

cp Dockerfile.builder build-inner.sh "${TEMP_DIR}/"
cd "${TEMP_DIR}"

# Builder image management
BUILDER_IMAGE="elchi-backend-arm64-builder"
echo "🔍 Checking builder image status..."

if docker image inspect ${BUILDER_IMAGE} >/dev/null 2>&1; then
    read -p "⚠️  Builder image already exists. Do you want to rebuild it? (y/N): " rebuild
    if [[ $rebuild =~ ^[Yy]$ ]]; then
        echo "🗑️  Removing existing builder image..."
        docker rmi ${BUILDER_IMAGE} || true
        echo "🏗️  Building fresh builder image..."
        docker build -t ${BUILDER_IMAGE} -f Dockerfile.builder .
    else
        echo "✅ Using existing builder image"
    fi
else
    echo "🏗️  Building builder image for the first time..."
    docker build -t ${BUILDER_IMAGE} -f Dockerfile.builder .
fi

# Run the builder with Docker socket mounted and environment variables
docker run \
    --rm \
    --platform linux/arm64 \
    -v /var/run/docker.sock:/var/run/docker.sock \
    -v "${DOCKER_CONFIG_DIR}:/root/.docker" \
    -v "${DOCKER_BUILDX_DIR}:/root/.docker/buildx" \
    -e DOCKER_USERNAME \
    -e DOCKER_PASSWORD \
    -e DOCKER_IMAGE="elchi-backend" \
    -e GITHUB_TOKEN \
    ${BUILDER_IMAGE} "$1"

# Cleanup
cd - > /dev/null
rm -rf "${TEMP_DIR}"
rm -rf "${DOCKER_CONFIG_DIR}"
rm -rf "${DOCKER_BUILDX_DIR}"