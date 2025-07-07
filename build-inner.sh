#!/bin/bash
set -e

CONTROL_PLANE_VERSIONS=$1

# Create fresh workspace
rm -rf /build 2>/dev/null || true
mkdir -p /build
cd /build

# Clone the repository and checkout the latest release
git clone https://${GITHUB_TOKEN}@github.com/CloudNativeWorks/elchi-backend.git /build
PROJECT_VERSION=$(cat VERSION)
git checkout "v${PROJECT_VERSION}"

# Go Control Plane package info
GO_CONTROL_PLANE_PACKAGE="github.com/CloudNativeWorks/versioned-go-control-plane"
GO_CONTROL_PLANE_ENVOY_PACKAGE="github.com/CloudNativeWorks/versioned-go-control-plane/envoy"

# Build for each version
IFS=',' read -ra VERSIONS <<< "${CONTROL_PLANE_VERSIONS}"
for CONTROL_PLANE_VERSION in "${VERSIONS[@]}"; do
    # Trim whitespace
    CONTROL_PLANE_VERSION=$(echo $CONTROL_PLANE_VERSION | xargs)
    
    echo "🔄 Processing: ${CONTROL_PLANE_VERSION}"
    
    # Extract Envoy version
    ENVOY_VERSION=$(echo $CONTROL_PLANE_VERSION | sed -n 's/.*envoy\([0-9.]*\)/\1/p')
    
    # Update Go modules
    go mod edit -require="${GO_CONTROL_PLANE_PACKAGE}@${CONTROL_PLANE_VERSION}"
    go mod edit -require="${GO_CONTROL_PLANE_ENVOY_PACKAGE}@v${ENVOY_VERSION}"
    go mod tidy
    
    # Prepare image tags
    IMAGE_NAME="${DOCKER_USERNAME}/${DOCKER_IMAGE}"
    IMAGE_TAG="${IMAGE_NAME}:v${PROJECT_VERSION}-${CONTROL_PLANE_VERSION}-arm64"
    LATEST_TAG="${IMAGE_NAME}:latest-arm64"
    
    echo "🏗️ Building ARM64 image: ${IMAGE_TAG}"
    
    # Docker build and push
    docker buildx build \
        --no-cache \
        --platform linux/arm64 \
        --build-arg "ENVOY_VERSION=v${ENVOY_VERSION}" \
        --build-arg "ELCHI_CONTROL_PLANE_VERSION=elchi-backend-${PROJECT_VERSION}-${CONTROL_PLANE_VERSION}" \
        -t "${IMAGE_TAG}" \
        -t "${LATEST_TAG}" \
        -f Dockerfile-release \
        --push \
        .
    
    echo "✅ Completed: ${CONTROL_PLANE_VERSION}"
    
    # Reset Go mod changes
    git checkout -- go.mod go.sum
done

echo "🎉 All ARM64 builds completed!"
