---
name: itup-deployment
description: Notes and learnings from deploying CAPOA+CAPK tenant clusters on the ITUP external infra cluster (prod-scale-spoke1-dc-rdu3). Use when "itup" is mentioned, when debugging ITUP deployments, or when deploying tenant clusters to the ITUP infrastructure.
---

# ITUP Deployment Notes

Detailed deployment notes, constraints, workarounds, and lessons learned for deploying CAPOA+CAPK tenant clusters on the ITUP external infra cluster are stored at:

```
/home/ocohen/temp/cnv2-engineering/capoa/ITUP-DEPLOYMENT-NOTES.md
```

## When to Read

Read this file whenever:
- The user mentions "itup" or "ITUP" in the context of deployments
- Debugging tenant cluster issues on the ITUP infra cluster
- Creating new manifests or deployments targeting the ITUP cluster
- Troubleshooting NTP, storage quota, LimitRange, or token issues on ITUP

## Quick Reference

- **Infra cluster**: `api.prod-scale-spoke1-dc-rdu3.prod-scale-mgmthub1-dc-rdu3.itup.redhat.com:6443`
- **Namespace**: `cnv-qe-devops-paas--runtime-int`
- **Permissions**: Project-admin only (no cluster-admin)
- **Storage**: `trident-nfs` (poor etcd performance)
- **NTP fix required**: `additionalNTPSources: ["clock.redhat.com","10.2.32.38","10.11.160.238"]`
- **Manifest**: `/home/ocohen/temp/cnv2-engineering/capoa/kubevirt-tenant-deploy-itup.yaml`
