include $(TOPDIR)/rules.mk

PKG_NAME:=karbiter
PKG_VERSION:=0.1.0
PKG_RELEASE:=1
PKG_LICENSE:=MIT

PKG_SOURCE_PROTO:=git
PKG_SOURCE_URL:=https://github.com/bmanojlovic/karbiter.git
PKG_SOURCE_VERSION:=HEAD

include $(INCLUDE_DIR)/package.mk

define Package/karbiter
  SECTION:=net
  CATEGORY:=Network
  TITLE:=Minimal Kubernetes Lease API server
  URL:=https://github.com/bmanojlovic/karbiter
  DEPENDS:=+ca-bundle
endef

define Package/karbiter/description
  A minimal Kubernetes Lease API server for external leader election.
  Single binary, implements coordination.k8s.io/v1 Lease API.
endef

define Build/Compile
	cd $(PKG_BUILD_DIR) && \
	GOOS=linux GOARCH=$(GO_ARCH) CGO_ENABLED=0 \
	go build -ldflags="-s -w" -o $(PKG_BUILD_DIR)/karbiter .
endef

define Package/karbiter/install
	$(INSTALL_DIR) $(1)/usr/bin
	$(INSTALL_BIN) $(PKG_BUILD_DIR)/karbiter $(1)/usr/bin/karbiter
	$(INSTALL_DIR) $(1)/etc/init.d
	$(INSTALL_BIN) ./files/karbiter.init $(1)/etc/init.d/karbiter
	$(INSTALL_DIR) $(1)/etc/config
	$(INSTALL_CONF) ./files/karbiter.config $(1)/etc/config/karbiter
endef

$(eval $(call BuildPackage,karbiter))
