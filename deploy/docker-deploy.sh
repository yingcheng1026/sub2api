#!/bin/bash
# =============================================================================
# Sub2API Docker Deployment Preparation Script
# =============================================================================
# This script prepares deployment files for Sub2API:
#   - Copies the reviewed deployment files shipped beside this script
#   - Generates all required application and database secrets
#   - Creates necessary data directories
#
# After running this script, you can start services with:
#   docker-compose up -d
# =============================================================================

set -euo pipefail
umask 077

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# Print colored message
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Generate random secret
generate_secret() {
    openssl rand -hex 32
}

# Check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Main installation function
main() {
    echo ""
    echo "=========================================="
    echo "  Sub2API Deployment Preparation"
    echo "=========================================="
    echo ""

    # Check if openssl is available
    if ! command_exists openssl; then
        print_error "openssl is not installed. Please install openssl first."
        exit 1
    fi

    # Check if deployment already exists
    if [ -f "docker-compose.yml" ] && [ -f ".env" ]; then
        print_warning "Deployment files already exist in current directory."
        read -p "Overwrite existing files? (y/N): " -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            print_info "Cancelled."
            exit 0
        fi
    fi

    # Use only files from the same reviewed checkout. Fetching mutable GitHub
    # main at deploy time would allow an unreviewed compose file to execute.
    if [ ! -f "${SCRIPT_DIR}/docker-compose.local.yml" ] || [ ! -f "${SCRIPT_DIR}/.env.example" ]; then
        print_error "Reviewed docker-compose.local.yml and .env.example must be beside this script."
        print_error "Clone or extract a reviewed release, then run deploy/docker-deploy.sh locally."
        exit 1
    fi
    print_info "Copying reviewed docker-compose.yml..."
    cp "${SCRIPT_DIR}/docker-compose.local.yml" docker-compose.yml
    print_success "Copied docker-compose.yml"

    # Generate .env file with auto-generated secrets
    print_info "Generating secure secrets..."
    echo ""

    # Generate secrets
    JWT_SECRET=$(generate_secret)
    SECRET_ENCRYPTION_TOTP_SECRET_KEY=$(generate_secret)
    SECRET_ENCRYPTION_TOTP_CACHE_KEY=$(generate_secret)
    SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY=$(generate_secret)
    SECRET_ENCRYPTION_BACKUP_S3_KEY=$(generate_secret)
    SECRET_ENCRYPTION_CONTENT_MODERATION_KEY=$(generate_secret)
    SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY=$(generate_secret)
    SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY=$(generate_secret)
    SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY=$(generate_secret)
    SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY=$(generate_secret)
    SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY=$(generate_secret)
    SECRET_ENCRYPTION_JWT_HMAC_KEY=$(generate_secret)
    SECRET_ENCRYPTION_SETTING_SECRET_KEY=$(generate_secret)
    API_KEY_ENCRYPTION_KEY=$(generate_secret)
    PAYMENT_RESUME_SIGNING_KEY=$(generate_secret)
    POSTGRES_PASSWORD=$(generate_secret)
    REDIS_PASSWORD=$(generate_secret)
    ADMIN_PASSWORD=$(generate_secret)

    # Create .env from .env.example
    cp "${SCRIPT_DIR}/.env.example" .env

    # Update .env with generated secrets (cross-platform compatible)
    if sed --version >/dev/null 2>&1; then
        # GNU sed (Linux)
        sed -i "s/^JWT_SECRET=.*/JWT_SECRET=${JWT_SECRET}/" .env
        sed -i "s/^SECRET_ENCRYPTION_TOTP_SECRET_KEY=.*/SECRET_ENCRYPTION_TOTP_SECRET_KEY=${SECRET_ENCRYPTION_TOTP_SECRET_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_TOTP_CACHE_KEY=.*/SECRET_ENCRYPTION_TOTP_CACHE_KEY=${SECRET_ENCRYPTION_TOTP_CACHE_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY=.*/SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY=${SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_BACKUP_S3_KEY=.*/SECRET_ENCRYPTION_BACKUP_S3_KEY=${SECRET_ENCRYPTION_BACKUP_S3_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_CONTENT_MODERATION_KEY=.*/SECRET_ENCRYPTION_CONTENT_MODERATION_KEY=${SECRET_ENCRYPTION_CONTENT_MODERATION_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY=.*/SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY=${SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY=.*/SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY=${SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY=.*/SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY=${SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY=.*/SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY=${SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY=.*/SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY=${SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_JWT_HMAC_KEY=.*/SECRET_ENCRYPTION_JWT_HMAC_KEY=${SECRET_ENCRYPTION_JWT_HMAC_KEY}/" .env
        sed -i "s/^SECRET_ENCRYPTION_SETTING_SECRET_KEY=.*/SECRET_ENCRYPTION_SETTING_SECRET_KEY=${SECRET_ENCRYPTION_SETTING_SECRET_KEY}/" .env
        sed -i "s/^API_KEY_ENCRYPTION_KEY=.*/API_KEY_ENCRYPTION_KEY=${API_KEY_ENCRYPTION_KEY}/" .env
        sed -i "s/^PAYMENT_RESUME_SIGNING_KEY=.*/PAYMENT_RESUME_SIGNING_KEY=${PAYMENT_RESUME_SIGNING_KEY}/" .env
        sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=${POSTGRES_PASSWORD}/" .env
        sed -i "s/^REDIS_PASSWORD=.*/REDIS_PASSWORD=${REDIS_PASSWORD}/" .env
        sed -i "s/^ADMIN_PASSWORD=.*/ADMIN_PASSWORD=${ADMIN_PASSWORD}/" .env
    else
        # BSD sed (macOS)
        sed -i '' "s/^JWT_SECRET=.*/JWT_SECRET=${JWT_SECRET}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_TOTP_SECRET_KEY=.*/SECRET_ENCRYPTION_TOTP_SECRET_KEY=${SECRET_ENCRYPTION_TOTP_SECRET_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_TOTP_CACHE_KEY=.*/SECRET_ENCRYPTION_TOTP_CACHE_KEY=${SECRET_ENCRYPTION_TOTP_CACHE_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY=.*/SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY=${SECRET_ENCRYPTION_ACCOUNT_CREDENTIAL_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_BACKUP_S3_KEY=.*/SECRET_ENCRYPTION_BACKUP_S3_KEY=${SECRET_ENCRYPTION_BACKUP_S3_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_CONTENT_MODERATION_KEY=.*/SECRET_ENCRYPTION_CONTENT_MODERATION_KEY=${SECRET_ENCRYPTION_CONTENT_MODERATION_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY=.*/SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY=${SECRET_ENCRYPTION_CHANNEL_MONITOR_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY=.*/SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY=${SECRET_ENCRYPTION_PAYMENT_PROVIDER_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY=.*/SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY=${SECRET_ENCRYPTION_PROXY_CREDENTIAL_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY=.*/SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY=${SECRET_ENCRYPTION_SCHEDULER_CACHE_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY=.*/SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY=${SECRET_ENCRYPTION_OAUTH_TOKEN_CACHE_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_JWT_HMAC_KEY=.*/SECRET_ENCRYPTION_JWT_HMAC_KEY=${SECRET_ENCRYPTION_JWT_HMAC_KEY}/" .env
        sed -i '' "s/^SECRET_ENCRYPTION_SETTING_SECRET_KEY=.*/SECRET_ENCRYPTION_SETTING_SECRET_KEY=${SECRET_ENCRYPTION_SETTING_SECRET_KEY}/" .env
        sed -i '' "s/^API_KEY_ENCRYPTION_KEY=.*/API_KEY_ENCRYPTION_KEY=${API_KEY_ENCRYPTION_KEY}/" .env
        sed -i '' "s/^PAYMENT_RESUME_SIGNING_KEY=.*/PAYMENT_RESUME_SIGNING_KEY=${PAYMENT_RESUME_SIGNING_KEY}/" .env
        sed -i '' "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=${POSTGRES_PASSWORD}/" .env
        sed -i '' "s/^REDIS_PASSWORD=.*/REDIS_PASSWORD=${REDIS_PASSWORD}/" .env
        sed -i '' "s/^ADMIN_PASSWORD=.*/ADMIN_PASSWORD=${ADMIN_PASSWORD}/" .env
    fi

    # Create data directories
    print_info "Creating data directories..."
    mkdir -p data postgres_data redis_data
    print_success "Created data directories"

    # Set secure permissions for .env file (readable/writable only by owner)
    chmod 600 .env
    echo ""

    # Display completion message
    echo "=========================================="
    echo "  Preparation Complete!"
    echo "=========================================="
    echo ""
    print_success "Generated credentials were written to the owner-only .env file (mode 600)."
    print_warning "Do not print, share, or commit .env. Back it up in an approved secret store."
    echo ""
    echo "Directory structure:"
    echo "  docker-compose.yml        - Docker Compose configuration"
    echo "  .env                      - Environment variables (generated secrets)"
    echo "  .env.example              - Example template (for reference)"
    echo "  data/                     - Application data (will be created on first run)"
    echo "  postgres_data/            - PostgreSQL data"
    echo "  redis_data/               - Redis data"
    echo ""
    echo "Next steps:"
    echo "  1. (Optional) Edit .env to customize configuration"
    echo "  2. Start services:"
    echo "     docker-compose up -d"
    echo ""
    echo "  3. View logs:"
    echo "     docker-compose logs -f sub2api"
    echo ""
    echo "  4. Access Web UI:"
    echo "     http://localhost:8080"
    echo ""
    print_info "The generated admin password is stored only in .env; it is not written to logs."
    echo ""
}

# Run main function
main "$@"
