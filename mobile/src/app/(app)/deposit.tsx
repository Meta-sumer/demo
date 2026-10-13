import { TransferPage } from '../../features/transfer/Transfer';

/** /deposit - the manual deposit claim as a screen of its own. */
export default function DepositRoute() {
  return <TransferPage kind="DEPOSIT" />;
}
