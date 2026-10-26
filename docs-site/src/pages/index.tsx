import { Redirect } from "@docusaurus/router";
import useBaseUrl from "@docusaurus/useBaseUrl";

// docs/ has no index page, the product spec is the entry point.
export default function Home() {
  return <Redirect to={useBaseUrl("/spec")} />;
}
