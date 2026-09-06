#!/usr/bin/env bash
#
# Hospitus Integration Test Runner
#
# Usage:
#   ./test/run-tests.sh                  # Run quick tests only
#   ./test/run-tests.sh --all            # Run all tests (including long ones)
#   ./test/run-tests.sh --long           # Run long tests only
#   ./test/run-tests.sh --pattern "Alpine"  # Run tests matching pattern
#

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default settings
RUN_LONG_TESTS=0
TEST_PATTERN=""
VERBOSE=0

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --all|-a)
            RUN_LONG_TESTS=1
            shift
            ;;
        --long|-l)
            RUN_LONG_TESTS=1
            shift
            ;;
        --pattern|-p)
            TEST_PATTERN="$2"
            shift 2
            ;;
        --verbose|-v)
            VERBOSE=1
            shift
            ;;
        --help|-h)
            echo "Hospitus Integration Test Runner"
            echo ""
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --all, -a        Run all tests including long-running ones"
            echo "  --long, -l       Same as --all"
            echo "  --pattern, -p    Run only tests matching pattern"
            echo "  --verbose, -v    Verbose output"
            echo "  --help, -h       Show this help"
            echo ""
            echo "Test Categories:"
            echo "  Image tests      - Download and catalog tests"
            echo "  Jail tests       - FreeBSD native jail tests"
            echo "  Linux tests      - Alpine, Ubuntu, Fedora jail tests"
            echo "  Expose tests     - Port forwarding tests"
            echo "  Service tests    - Nginx, PostgreSQL tests"
            echo ""
            echo "Examples:"
            echo "  $0                           # Quick tests"
            echo "  $0 --all                     # All tests"
            echo "  $0 --pattern TestAlpine      # Only Alpine tests"
            echo "  $0 --pattern TestImage       # Only image tests"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# Print header
echo -e "${BLUE}============================================${NC}"
echo -e "${BLUE}        Hospitus Integration Tests${NC}"
echo -e "${BLUE}============================================${NC}"
echo ""

# Check if running as root
if [[ $EUID -ne 0 ]]; then
    echo -e "${YELLOW}Warning: Integration tests require root privileges${NC}"
    echo "Please run: doas $0 $@"
    exit 1
fi

# Change to project directory
cd "$PROJECT_DIR"

# Build binaries if needed
echo -e "${BLUE}[1/4] Checking binaries...${NC}"
if [[ ! -f "./hospitus" ]] || [[ ! -f "./hospitusd" ]]; then
    echo "Building binaries..."
    go build -o hospitus ./cmd/hospitus-cli/
    go build -o hospitusd ./cmd/hospitusd/
fi

# Check daemon
echo -e "${BLUE}[2/4] Checking daemon...${NC}"
if ! curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:8080/health 2>/dev/null | grep -q "200"; then
    echo "Starting hospitusd daemon..."
    ./hospitusd --data-dir /var/lib/hospitus --state-dir /var/lib/hospitus/state --db /var/lib/hospitus/hospitus.db &
    DAEMON_PID=$!
    sleep 3

    if ! curl -s -o /dev/null http://127.0.0.1:8080/health 2>/dev/null; then
        echo -e "${RED}Failed to start daemon${NC}"
        exit 1
    fi
    echo "Daemon started (PID: $DAEMON_PID)"
else
    echo "Daemon already running"
fi

# Check Linux compatibility
echo -e "${BLUE}[3/4] Checking environment...${NC}"
if kldstat -q -m linux64elf 2>/dev/null; then
    echo "  Linux compatibility: enabled"
    LINUX_COMPAT=1
else
    echo -e "  ${YELLOW}Linux compatibility: disabled (Linux jail tests will be skipped)${NC}"
    LINUX_COMPAT=0
fi

# Check RCTL
if sysctl -n kern.racct.enable 2>/dev/null | grep -q "1"; then
    echo "  RCTL: enabled"
else
    echo -e "  ${YELLOW}RCTL: disabled (resource limit tests may not work)${NC}"
fi

# Prepare test command
echo -e "${BLUE}[4/4] Running tests...${NC}"
echo ""

TEST_ARGS="-v"

if [[ $RUN_LONG_TESTS -eq 1 ]]; then
    export HOSPITUS_LONG_TESTS=1
    echo -e "${YELLOW}Running ALL tests (including long-running ones)${NC}"
else
    echo "Running quick tests only (use --all for full suite)"
fi

if [[ -n "$TEST_PATTERN" ]]; then
    TEST_ARGS="$TEST_ARGS -run $TEST_PATTERN"
    echo "Pattern: $TEST_PATTERN"
fi

echo ""
echo "============================================"
echo ""

# Run tests from project root
cd "$PROJECT_DIR"

if [[ $VERBOSE -eq 1 ]]; then
    go test $TEST_ARGS ./test/integration/...
else
    go test $TEST_ARGS ./test/integration/... 2>&1 | while IFS= read -r line; do
        if [[ "$line" == *"PASS"* ]]; then
            echo -e "${GREEN}$line${NC}"
        elif [[ "$line" == *"FAIL"* ]]; then
            echo -e "${RED}$line${NC}"
        elif [[ "$line" == *"SKIP"* ]]; then
            echo -e "${YELLOW}$line${NC}"
        else
            echo "$line"
        fi
    done
fi

TEST_EXIT=$?

echo ""
echo "============================================"

if [[ $TEST_EXIT -eq 0 ]]; then
    echo -e "${GREEN}All tests passed!${NC}"
else
    echo -e "${RED}Some tests failed${NC}"
fi

exit $TEST_EXIT
