#!/bin/bash

# --- Configuration ---
GO_VERSION="1.26.2"
GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"
GO_DOWNLOAD_URL="https://go.dev/dl/${GO_TARBALL}"

echo "🚀 Starting installation: Go ${GO_VERSION} and Build Tools..."

# 1. Update system and install 'make' (via build-essential)
echo "📦 Updating system and installing build-essential (make, gcc, etc.)..."
sudo apt update && sudo apt install -y build-essential curl wget

# 2. Clean up old Go installation
echo "🧹 Removing any existing Go installation at /usr/local/go..."
sudo rm -rf /usr/local/go

# 3. Download Go
echo "📥 Downloading Go ${GO_VERSION}..."
wget -q --show-progress $GO_DOWNLOAD_URL

# 4. Extract Go
echo "📂 Extracting to /usr/local..."
sudo tar -C /usr/local -xzf $GO_TARBALL

# 5. Set up Environment Variables
echo "⚙️ Configuring environment variables in ~/.bashrc..."

# Check if PATH is already configured to avoid duplicates
if ! grep -q "/usr/local/go/bin" ~/.bashrc; then
    echo '' >> ~/.bashrc
    echo '# Go Language Path' >> ~/.bashrc
    echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
    echo 'export GOPATH=$HOME/go' >> ~/.bashrc
    echo 'export PATH=$PATH:$GOPATH/bin' >> ~/.bashrc
fi

# 6. Cleanup
echo "🧼 Cleaning up download files..."
rm $GO_TARBALL

echo "✅ Installation complete!"
echo "👉 IMPORTANT: Run 'source ~/.bashrc' or restart your terminal to start using Go."