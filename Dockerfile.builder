FROM --platform=linux/arm64 golang:1.24

RUN apt-get update && apt-get install -y git

# Create directories
RUN mkdir -p /scripts /workspace

# Install Docker
RUN apt-get install -y apt-transport-https ca-certificates curl gnupg lsb-release && \
    curl -fsSL https://download.docker.com/linux/debian/gpg | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg && \
    echo "deb [arch=arm64 signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/debian \
    $(lsb_release -cs) stable" | tee /etc/apt/sources.list.d/docker.list > /dev/null && \
    apt-get update && \
    apt-get install -y docker-ce docker-ce-cli containerd.io

# Copy script to /scripts directory
COPY build-inner.sh /scripts/
RUN chmod +x /scripts/build-inner.sh

WORKDIR /workspace

ENTRYPOINT ["/scripts/build-inner.sh"]
