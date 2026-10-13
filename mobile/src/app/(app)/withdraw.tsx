import { TransferPage } from '../../features/transfer/Transfer';

/** /withdraw - the withdrawal request as a screen of its own. */
export default function WithdrawRoute() {
  return <TransferPage kind="WITHDRAWAL" />;
}
