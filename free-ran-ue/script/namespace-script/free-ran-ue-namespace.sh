#! /bin/bash

FREE_RAN_NAMESPACE="free-ran-ns"
FREE_UE_NAMESPACE="free-ue-ns"

usage() {
    echo "Usage: $0 [ up | down | ran-ns | ue-ns ]"
    echo "  up     - Setup both RAN and UE namespaces"
    echo "  down   - Cleanup both namespaces"
    echo "  ran-ns - Enter RAN namespace"
    echo "  ue-ns  - Enter UE namespace"
    exit 1
}

setup_network_namespace() {
    # Remove exist network namespace
    echo "Removing exist network namespace..."
    cleanup_network_namespace
    echo

    # Create network namespace
    echo "Creating network namespace..."
    sudo ip netns add $FREE_RAN_NAMESPACE 2>/dev/null || true
    sudo ip netns add $FREE_UE_NAMESPACE 2>/dev/null || true
    echo

    # Create veth pair
    echo "Creating veth pair..."
    sudo ip link add fnsHost type veth peer fnsVeth
    sudo ip link add fnsGnb type veth peer fnsUe
    echo

    # Move veth pair to network namespace
    echo "Moving veth pair to network namespace..."
    sudo ip link set fnsVeth netns $FREE_RAN_NAMESPACE
    sudo ip link set fnsGnb netns $FREE_RAN_NAMESPACE
    sudo ip link set fnsUe netns $FREE_UE_NAMESPACE
    echo

    # Bring up the interface in host namespace
    echo "Bring up the interface in host namespace..."
    sudo ip link set fnsHost up
    echo

    # Bring up the interface in free-ran-ue namespace
    echo "Bring up the interface in free-ran-ue namespace..."
    sudo ip netns exec $FREE_RAN_NAMESPACE ip link set fnsVeth up
    sudo ip netns exec $FREE_RAN_NAMESPACE ip link set fnsGnb up
    echo

    # Bring up the interface in free-ran-ue namespace
    echo "Bring up the interface in free-ran-ue namespace..."
    sudo ip netns exec $FREE_UE_NAMESPACE ip link set fnsUe up
    echo

    # Set up IP address
    echo "Setting up IP address..."
    # free-ran-ue namespace: 10.0.1.0/24 network
    sudo ip addr add 10.0.1.1/24 dev fnsHost
    sudo ip netns exec $FREE_RAN_NAMESPACE ip addr add 10.0.1.2/24 dev fnsVeth
    sudo ip netns exec $FREE_RAN_NAMESPACE ip addr add 10.0.2.1/24 dev fnsGnb
    sudo ip netns exec $FREE_UE_NAMESPACE ip addr add 10.0.2.2/24 dev fnsUe
    echo

    # Set up default route
    echo "Setting up default route..."
    sudo ip netns exec $FREE_RAN_NAMESPACE ip route add default via 10.0.1.1
    sudo ip netns exec $FREE_RAN_NAMESPACE ip route add 10.0.2.0/24 dev fnsGnb
    sudo ip netns exec $FREE_UE_NAMESPACE ip route add default via 10.0.2.1
    echo

    echo "$FREE_RAN_NAMESPACE namespace setup complete"
    echo "$FREE_UE_NAMESPACE namespace setup complete"
    echo "Network topology:"
    echo "  Host (10.0.1.1) <---> RAN namespace (10.0.1.2 | 10.0.2.1) <---> UE namespace (10.0.2.2)"
}

cleanup_network_namespace() {
    echo "Removing network namespace..."

    # Bring down interface
    sudo ip link set fnsHost down 2>/dev/null || true

    # Delete veth pair
    sudo ip link delete fnsHost 2>/dev/null || true
    sudo ip link delete fnsGnb 2>/dev/null || true

    # Delete network namespace
    sudo ip netns delete $FREE_RAN_NAMESPACE 2>/dev/null || true
    sudo ip netns delete $FREE_UE_NAMESPACE 2>/dev/null || true

    echo "$FREE_RAN_NAMESPACE namespace removed"
    echo "$FREE_UE_NAMESPACE namespace removed"
}

main() {
    if [ $# -ne 1 ]; then
        usage
    fi

    case "$1" in
        "up")
            setup_network_namespace
        ;;
        "down")
            cleanup_network_namespace
        ;;
        "ran-ns")
            sudo ip netns exec $FREE_RAN_NAMESPACE bash
        ;;
        "ue-ns")
            sudo ip netns exec $FREE_UE_NAMESPACE bash
        ;;
        *)
            usage
        ;;
    esac
}

main "$@"