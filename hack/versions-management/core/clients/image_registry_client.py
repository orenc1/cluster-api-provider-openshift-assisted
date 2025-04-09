import requests
import logging

logger = logging.getLogger(__name__)


class ImageRegistryClient:
    def __init__(self, registry_url: str = "https://quay.io"):
        self.registry_url: str = registry_url

    def exists(self, image: str, tag: str) -> bool:
        path = image.replace("quay.io/", "")
        url = f"{self.registry_url}/v2/{path}/manifests/{tag}"
        headers = {"Accept": "application/vnd.docker.distribution.manifest.v2+json"}
        try:
            response = requests.head(url=url, headers=headers, timeout=5)
            return response.status_code == 200
        except Exception as e:
            logger.error(f"Error checking image {image} existence: {e}")
            return False

    def resolve_digest(self, image: str, tag: str) -> str | None:
        path = image.replace("quay.io/", "")
        url = f"{self.registry_url}/v2/{path}/manifests/{tag}"
        headers = {"Accept": "application/vnd.docker.distribution.manifest.v2+json"}

        try:
            response = requests.get(url=url, headers=headers, timeout=5)
            if response.status_code == 200:
                digest = response.headers.get("Docker-Content-Digest")
                if digest:
                    return digest
                else:
                    logger.warning(f"No digest found in headers for {image}:{tag}")
            else:
                logger.warning(f"Failed to resolve digest: {image}:{tag} (status code {response.status_code})")
        except Exception as e:
            logger.error(f"Error resolving digest for {image}:{tag}: {e}")
        return None
