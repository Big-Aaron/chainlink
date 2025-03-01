package actions

//revive:disable:dot-imports
import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/lib/pq"
	"github.com/rs/zerolog/log"
	"github.com/smartcontractkit/chainlink-testing-framework/blockchain"
	"github.com/smartcontractkit/chainlink-testing-framework/contracts/ethereum"
	"github.com/smartcontractkit/libocr/offchainreporting2/confighelper"
	"github.com/smartcontractkit/libocr/offchainreporting2/types"
	types2 "github.com/smartcontractkit/ocr2keepers/pkg/types"
	"github.com/stretchr/testify/require"
	"gopkg.in/guregu/null.v4"

	"github.com/smartcontractkit/chainlink/core/services/job"
	"github.com/smartcontractkit/chainlink/core/services/keystore/chaintype"
	"github.com/smartcontractkit/chainlink/core/store/models"
	"github.com/smartcontractkit/chainlink/integration-tests/client"
	"github.com/smartcontractkit/chainlink/integration-tests/contracts"
)

func BuildAutoOCR2ConfigVars(
	t *testing.T,
	chainlinkNodes []*client.Chainlink,
	registryConfig contracts.KeeperRegistrySettings,
	registrar string,
	deltaStage time.Duration,
) contracts.OCRConfig {
	S, oracleIdentities := getOracleIdentities(t, chainlinkNodes)

	signerOnchainPublicKeys, transmitterAccounts, f, _, offchainConfigVersion, offchainConfig, err := confighelper.ContractSetConfigArgsForTests(
		5*time.Second,         // deltaProgress time.Duration,
		10*time.Second,        // deltaResend time.Duration,
		1000*time.Millisecond, // deltaRound time.Duration,
		20*time.Millisecond,   // deltaGrace time.Duration,
		deltaStage,            // deltaStage time.Duration,
		48,                    // rMax uint8,
		S,                     // s []int,
		oracleIdentities,      // oracles []OracleIdentityExtra,
		types2.OffchainConfig{
			TargetProbability:    "0.999",
			TargetInRounds:       1,
			PerformLockoutWindow: 100 * 12 * 1000, // ~100 block lockout (on goerli)
			GasLimitPerReport:    5_300_000,
			GasOverheadPerUpkeep: 300_000,
			SamplingJobDuration:  3000,
			MinConfirmations:     0,
			MaxUpkeepBatchSize:   20,
		}.Encode(), // reportingPluginConfig []byte,
		20*time.Millisecond,  // maxDurationQuery time.Duration,
		20*time.Millisecond,  // maxDurationObservation time.Duration,
		800*time.Millisecond, // maxDurationReport time.Duration,
		20*time.Millisecond,  // maxDurationShouldAcceptFinalizedReport time.Duration,
		20*time.Millisecond,  // maxDurationShouldTransmitAcceptedReport time.Duration,
		1,                    // f int,
		nil,                  // onchainConfig []byte,
	)
	require.NoError(t, err, "Shouldn't fail ContractSetConfigArgsForTests")

	var signers []common.Address
	for _, signer := range signerOnchainPublicKeys {
		require.Equal(t, 20, len(signer), "OnChainPublicKey has wrong length for address")
		signers = append(signers, common.BytesToAddress(signer))
	}

	var transmitters []common.Address
	for _, transmitter := range transmitterAccounts {
		require.True(t, common.IsHexAddress(string(transmitter)), "TransmitAccount is not a valid Ethereum address")
		transmitters = append(transmitters, common.HexToAddress(string(transmitter)))
	}

	onchainConfig, err := registryConfig.EncodeOnChainConfig(registrar)
	require.NoError(t, err, "Shouldn't fail encoding config")

	log.Info().Msg("Done building OCR config")
	return contracts.OCRConfig{
		Signers:               signers,
		Transmitters:          transmitters,
		F:                     f,
		OnchainConfig:         onchainConfig,
		OffchainConfigVersion: offchainConfigVersion,
		OffchainConfig:        offchainConfig,
	}
}

func getOracleIdentities(t *testing.T, chainlinkNodes []*client.Chainlink) ([]int, []confighelper.OracleIdentityExtra) {
	S := make([]int, len(chainlinkNodes))
	oracleIdentities := make([]confighelper.OracleIdentityExtra, len(chainlinkNodes))
	sharedSecretEncryptionPublicKeys := make([]types.ConfigEncryptionPublicKey, len(chainlinkNodes))
	var wg sync.WaitGroup
	for i, cl := range chainlinkNodes {
		wg.Add(1)
		go func(i int, cl *client.Chainlink) {
			defer wg.Done()

			address, err := cl.PrimaryEthAddress()
			require.NoError(t, err, "Shouldn't fail getting primary ETH address from OCR node: index %d", i)
			ocr2Keys, err := cl.MustReadOCR2Keys()
			require.NoError(t, err, "Shouldn't fail reading OCR2 keys from node")
			var ocr2Config client.OCR2KeyAttributes
			for _, key := range ocr2Keys.Data {
				if key.Attributes.ChainType == string(chaintype.EVM) {
					ocr2Config = key.Attributes
					break
				}
			}

			keys, err := cl.MustReadP2PKeys()
			require.NoError(t, err, "Shouldn't fail reading P2P keys from node")
			p2pKeyID := keys.Data[0].Attributes.PeerID

			offchainPkBytes, err := hex.DecodeString(strings.TrimPrefix(ocr2Config.OffChainPublicKey, "ocr2off_evm_"))
			require.NoError(t, err, "failed to decode %s: %v", ocr2Config.OffChainPublicKey, err)

			offchainPkBytesFixed := [ed25519.PublicKeySize]byte{}
			n := copy(offchainPkBytesFixed[:], offchainPkBytes)
			require.Equal(t, ed25519.PublicKeySize, n, "Wrong number of elements copied")

			configPkBytes, err := hex.DecodeString(strings.TrimPrefix(ocr2Config.ConfigPublicKey, "ocr2cfg_evm_"))
			require.NoError(t, err, "failed to decode %s: %v", ocr2Config.ConfigPublicKey, err)

			configPkBytesFixed := [ed25519.PublicKeySize]byte{}
			n = copy(configPkBytesFixed[:], configPkBytes)
			require.Equal(t, ed25519.PublicKeySize, n, "Wrong number of elements copied")

			onchainPkBytes, err := hex.DecodeString(strings.TrimPrefix(ocr2Config.OnChainPublicKey, "ocr2on_evm_"))
			require.NoError(t, err, "failed to decode %s: %v", ocr2Config.OnChainPublicKey, err)

			sharedSecretEncryptionPublicKeys[i] = configPkBytesFixed
			oracleIdentities[i] = confighelper.OracleIdentityExtra{
				OracleIdentity: confighelper.OracleIdentity{
					OnchainPublicKey:  onchainPkBytes,
					OffchainPublicKey: offchainPkBytesFixed,
					PeerID:            p2pKeyID,
					TransmitAccount:   types.Account(address),
				},
				ConfigEncryptionPublicKey: configPkBytesFixed,
			}
			S[i] = 1
		}(i, cl)
	}
	wg.Wait()
	log.Info().Msg("Done fetching oracle identities")
	return S, oracleIdentities
}

// CreateOCRKeeperJobs bootstraps the first node and to the other nodes sends ocr jobs
func CreateOCRKeeperJobs(
	t *testing.T,
	chainlinkNodes []*client.Chainlink,
	registryAddr string,
	chainID int64,
	keyIndex int,
) {
	bootstrapNode := chainlinkNodes[0]
	bootstrapNode.RemoteIP()
	bootstrapP2PIds, err := bootstrapNode.MustReadP2PKeys()
	require.NoError(t, err, "Shouldn't fail reading P2P keys from bootstrap node")
	bootstrapP2PId := bootstrapP2PIds.Data[0].Attributes.PeerID

	bootstrapSpec := &client.OCR2TaskJobSpec{
		Name:    "ocr2 bootstrap node",
		JobType: "bootstrap",
		OCR2OracleSpec: job.OCR2OracleSpec{
			ContractID: registryAddr,
			Relay:      "evm",
			RelayConfig: map[string]interface{}{
				"chainID": int(chainID),
			},
			ContractConfigTrackerPollInterval: *models.NewInterval(time.Second * 15),
		},
	}
	_, err = bootstrapNode.MustCreateJob(bootstrapSpec)
	require.NoError(t, err, "Shouldn't fail creating bootstrap job on bootstrap node")
	P2Pv2Bootstrapper := fmt.Sprintf("%s@%s:%d", bootstrapP2PId, bootstrapNode.RemoteIP(), 6690)

	for nodeIndex := 1; nodeIndex < len(chainlinkNodes); nodeIndex++ {
		nodeTransmitterAddress, err := chainlinkNodes[nodeIndex].EthAddresses()
		require.NoError(t, err, "Shouldn't fail getting primary ETH address from OCR node %d", nodeIndex+1)
		nodeOCRKeys, err := chainlinkNodes[nodeIndex].MustReadOCR2Keys()
		require.NoError(t, err, "Shouldn't fail getting OCR keys from OCR node %d", nodeIndex+1)
		var nodeOCRKeyId []string
		for _, key := range nodeOCRKeys.Data {
			if key.Attributes.ChainType == string(chaintype.EVM) {
				nodeOCRKeyId = append(nodeOCRKeyId, key.ID)
				break
			}
		}

		autoOCR2JobSpec := client.OCR2TaskJobSpec{
			Name:    "ocr2",
			JobType: "offchainreporting2",
			OCR2OracleSpec: job.OCR2OracleSpec{
				PluginType: "ocr2automation",
				Relay:      "evm",
				RelayConfig: map[string]interface{}{
					"chainID": int(chainID),
				},
				PluginConfig: map[string]interface{}{
					"maxServiceWorkers": 100,
				},
				ContractConfigTrackerPollInterval: *models.NewInterval(time.Second * 15),
				ContractID:                        registryAddr,                                      // registryAddr
				OCRKeyBundleID:                    null.StringFrom(nodeOCRKeyId[keyIndex]),           // get node ocr2config.ID
				TransmitterID:                     null.StringFrom(nodeTransmitterAddress[keyIndex]), // node addr
				P2PV2Bootstrappers:                pq.StringArray{P2Pv2Bootstrapper},                 // bootstrap node key and address <p2p-key>@bootstrap:8000
			},
		}

		_, err = chainlinkNodes[nodeIndex].MustCreateJob(&autoOCR2JobSpec)
		require.NoError(t, err, "Shouldn't fail creating OCR Task job on OCR node %d", nodeIndex+1)
	}
	log.Info().Msg("Done creating OCR automation jobs")
}

// DeployAutoOCRRegistryAndRegistrar registry and registrar
func DeployAutoOCRRegistryAndRegistrar(
	t *testing.T,
	registryVersion ethereum.KeeperRegistryVersion,
	registrySettings contracts.KeeperRegistrySettings,
	numberOfUpkeeps int,
	linkToken contracts.LinkToken,
	contractDeployer contracts.ContractDeployer,
	client blockchain.EVMClient,
) (contracts.KeeperRegistry, contracts.KeeperRegistrar) {
	registry := deployRegistry(t, registryVersion, registrySettings, contractDeployer, client, linkToken)

	// Fund the registry with 1 LINK * amount of KeeperConsumerPerformance contracts
	err := linkToken.Transfer(registry.Address(), big.NewInt(0).Mul(big.NewInt(1e18), big.NewInt(int64(numberOfUpkeeps))))
	require.NoError(t, err, "Funding keeper registry contract shouldn't fail")

	registrar := deployRegistrar(t, registryVersion, registry, linkToken, contractDeployer, client)

	return registry, registrar
}

func DeployConsumers(
	t *testing.T,
	registry contracts.KeeperRegistry,
	registrar contracts.KeeperRegistrar,
	linkToken contracts.LinkToken,
	contractDeployer contracts.ContractDeployer,
	client blockchain.EVMClient,
	numberOfUpkeeps int,
	linkFundsForEachUpkeep *big.Int,
	upkeepGasLimit uint32,
) ([]contracts.KeeperConsumer, []*big.Int) {
	upkeeps := DeployKeeperConsumers(t, contractDeployer, client, numberOfUpkeeps)
	var upkeepsAddresses []string
	for _, upkeep := range upkeeps {
		upkeepsAddresses = append(upkeepsAddresses, upkeep.Address())
	}
	upkeepIds := RegisterUpkeepContracts(
		t, linkToken, linkFundsForEachUpkeep, client, upkeepGasLimit, registry, registrar, numberOfUpkeeps, upkeepsAddresses,
	)
	return upkeeps, upkeepIds
}

func DeployPerformanceConsumers(
	t *testing.T,
	registry contracts.KeeperRegistry,
	registrar contracts.KeeperRegistrar,
	linkToken contracts.LinkToken,
	contractDeployer contracts.ContractDeployer,
	client blockchain.EVMClient,
	numberOfUpkeeps int,
	linkFundsForEachUpkeep *big.Int,
	upkeepGasLimit uint32,
	blockRange, // How many blocks to run the test for
	blockInterval, // Interval of blocks that upkeeps are expected to be performed
	checkGasToBurn, // How much gas should be burned on checkUpkeep() calls
	performGasToBurn int64, // How much gas should be burned on performUpkeep() calls
) ([]contracts.KeeperConsumerPerformance, []*big.Int) {
	upkeeps := DeployKeeperConsumersPerformance(
		t, contractDeployer, client, numberOfUpkeeps, blockRange, blockInterval, checkGasToBurn, performGasToBurn,
	)
	var upkeepsAddresses []string
	for _, upkeep := range upkeeps {
		upkeepsAddresses = append(upkeepsAddresses, upkeep.Address())
	}
	upkeepIds := RegisterUpkeepContracts(
		t, linkToken, linkFundsForEachUpkeep, client, upkeepGasLimit, registry, registrar, numberOfUpkeeps, upkeepsAddresses,
	)
	return upkeeps, upkeepIds
}

func DeployPerformDataCheckerConsumers(
	t *testing.T,
	registry contracts.KeeperRegistry,
	registrar contracts.KeeperRegistrar,
	linkToken contracts.LinkToken,
	contractDeployer contracts.ContractDeployer,
	client blockchain.EVMClient,
	numberOfUpkeeps int,
	linkFundsForEachUpkeep *big.Int,
	upkeepGasLimit uint32,
	expectedData []byte,
) ([]contracts.KeeperPerformDataChecker, []*big.Int) {
	upkeeps := DeployPerformDataChecker(t, contractDeployer, client, numberOfUpkeeps, expectedData)
	var upkeepsAddresses []string
	for _, upkeep := range upkeeps {
		upkeepsAddresses = append(upkeepsAddresses, upkeep.Address())
	}
	upkeepIds := RegisterUpkeepContracts(
		t, linkToken, linkFundsForEachUpkeep, client, upkeepGasLimit, registry, registrar, numberOfUpkeeps, upkeepsAddresses,
	)
	return upkeeps, upkeepIds
}

func deployRegistrar(
	t *testing.T,
	registryVersion ethereum.KeeperRegistryVersion,
	registry contracts.KeeperRegistry,
	linkToken contracts.LinkToken,
	contractDeployer contracts.ContractDeployer,
	client blockchain.EVMClient,
) contracts.KeeperRegistrar {
	registrarSettings := contracts.KeeperRegistrarSettings{
		AutoApproveConfigType: 2,
		AutoApproveMaxAllowed: math.MaxUint16,
		RegistryAddr:          registry.Address(),
		MinLinkJuels:          big.NewInt(0),
	}
	registrar, err := contractDeployer.DeployKeeperRegistrar(registryVersion, linkToken.Address(), registrarSettings)
	require.NoError(t, err, "Deploying KeeperRegistrar contract shouldn't fail")
	err = client.WaitForEvents()
	require.NoError(t, err, "Failed waiting for registrar to deploy")
	return registrar
}

func deployRegistry(
	t *testing.T,
	registryVersion ethereum.KeeperRegistryVersion,
	registrySettings contracts.KeeperRegistrySettings,
	contractDeployer contracts.ContractDeployer,
	client blockchain.EVMClient,
	linkToken contracts.LinkToken,
) contracts.KeeperRegistry {
	ef, err := contractDeployer.DeployMockETHLINKFeed(big.NewInt(2e18))
	require.NoError(t, err, "Deploying mock ETH-Link feed shouldn't fail")
	gf, err := contractDeployer.DeployMockGasFeed(big.NewInt(2e11))
	require.NoError(t, err, "Deploying mock gas feed shouldn't fail")
	err = client.WaitForEvents()
	require.NoError(t, err, "Failed waiting for mock feeds to deploy")

	// Deploy the transcoder here, and then set it to the registry
	transcoder := DeployUpkeepTranscoder(t, contractDeployer, client)
	registry := DeployKeeperRegistry(t, contractDeployer, client,
		&contracts.KeeperRegistryOpts{
			RegistryVersion: registryVersion,
			LinkAddr:        linkToken.Address(),
			ETHFeedAddr:     ef.Address(),
			GasFeedAddr:     gf.Address(),
			TranscoderAddr:  transcoder.Address(),
			RegistrarAddr:   ZeroAddress.Hex(),
			Settings:        registrySettings,
		},
	)
	return registry
}
